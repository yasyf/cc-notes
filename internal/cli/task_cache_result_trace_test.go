package cli_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func taskTraceFDResult(value string) (int64, error) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, errors.New("empty traced FD result")
	}
	number, path, annotated := strings.Cut(value, "<")
	if !annotated {
		number = fields[0]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(number), 10, 64)
	if err != nil {
		return 0, err
	}
	if annotated && (n < 0 || len(path) < 2 || !strings.HasSuffix(path, ">")) {
		return 0, fmt.Errorf("invalid traced FD result %q", value)
	}
	return n, nil
}

func taskListResultTrace(r io.Reader, outputPath string, roots map[string]bool) ([]byte, []byte, int, int, error) {
	var operation, tail bytes.Buffer
	pending := make(map[string]string)
	boundary, unknown := false, 0
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if boundary {
			tail.WriteString(line + "\n")
		}
		pid, event, ok := strings.Cut(line, " ")
		if !ok {
			return nil, nil, 0, 0, fmt.Errorf("missing trace PID: %q", line)
		}
		event = strings.TrimSpace(event)
		if strings.HasPrefix(event, "--- ") || strings.HasPrefix(event, "+++ ") {
			continue
		}
		if boundary && event == "???( <detached ...>" {
			unknown++
			continue
		}
		if prefix, ok := strings.CutSuffix(event, "<unfinished ...>"); ok {
			if pending[pid] != "" {
				return nil, nil, 0, 0, fmt.Errorf("overlapping unfinished syscall: %q", line)
			}
			pending[pid] = prefix
			if boundary && (strings.HasPrefix(prefix, "open(") || strings.HasPrefix(prefix, "openat(") || strings.HasPrefix(prefix, "openat2(")) {
				name, args, _ := strings.Cut(prefix, "(")
				path, err := taskOpenPath(name, args)
				if err != nil {
					return nil, nil, 0, 0, err
				}
				if taskTraceRoot(path, roots) {
					return nil, nil, 0, 0, fmt.Errorf("root lookup after result: %q", line)
				}
			}
			continue
		}
		if strings.HasPrefix(event, "<... ") {
			name, suffix, ok := strings.Cut(strings.TrimPrefix(event, "<... "), " resumed>")
			if !ok || !strings.HasPrefix(pending[pid], name+"(") {
				return nil, nil, 0, 0, fmt.Errorf("unpaired resumed syscall: %q", line)
			}
			event = pending[pid] + suffix
			delete(pending, pid)
		}
		name, _, ok := strings.Cut(event, "(")
		result := strings.LastIndex(event, " = ")
		if !ok || result < len(name)+1 {
			return nil, nil, 0, 0, fmt.Errorf("missing syscall frame: %q", line)
		}
		args, fields := event[len(name)+1:result], strings.Fields(event[result+3:])
		if len(fields) == 0 {
			return nil, nil, 0, 0, errors.New("empty syscall result")
		}
		if name == "write" {
			if len(fields) > 1 && fields[0] == "?" && fields[1] == "ERESTARTSYS" {
				continue
			}
			n, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil {
				return nil, nil, 0, 0, err
			}
			fd, _, _ := strings.Cut(args, ",")
			if !boundary && fd == "1<"+outputPath+">" && n > 0 {
				if len(pending) != 0 {
					return nil, nil, 0, 0, fmt.Errorf("syscalls cross result boundary: %v", pending)
				}
				boundary = true
			}
			continue
		}
		frame := pid + " " + event + "\n"
		lookedUp, err := parseTaskCacheLookups(strings.NewReader(frame), roots)
		if err != nil {
			return nil, nil, 0, 0, err
		}
		if boundary {
			if len(lookedUp) != 0 {
				return nil, nil, 0, 0, fmt.Errorf("root lookup after result: %q", line)
			}
		} else {
			operation.WriteString(frame)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, 0, 0, err
	}
	if !boundary {
		return nil, nil, 0, 0, errors.New("missing successful exact stdout-file write")
	}
	return operation.Bytes(), tail.Bytes(), unknown, len(pending), nil
}

func taskTraceRoot(path string, roots map[string]bool) bool {
	path = filepath.Clean(path)
	parent, base := filepath.Base(filepath.Dir(path)), filepath.Base(path)
	return len(parent) == 2 && len(base) == 38 && roots[parent+base]
}

func TestTaskCacheResultBoundaryAccounting(t *testing.T) {
	root := strings.Repeat("a", 40)
	roots := map[string]bool{root: true}
	open := fmt.Sprintf("101 openat(AT_FDCWD</tmp>, %q, O_RDONLY) = -1 ENOENT (missing)\n", "/tmp/objects/aa/"+root[2:])
	marker := "101 write(1</private/output>, \"row\\n\", 4) = 4\n"
	unknown := "102 ???( <detached ...>\n"
	op, tail, count, pending, err := taskListResultTrace(strings.NewReader(open+marker+unknown), "/private/output", roots)
	if err != nil || string(op) != open || string(tail) != unknown || count != 1 || pending != 0 {
		t.Fatalf("operation=%q tail=%q unknown=%d pending=%d err=%v", op, tail, count, pending, err)
	}
	resumed := "101 write(1</private/output>, \"row\\n\", 4 <unfinished ...>\n101 <... write resumed>) = 4\n"
	late := "103 openat(AT_FDCWD</tmp>, \"/tmp/unrelated\", O_RDONLY) = 3</tmp/unrelated>\n"
	op, tail, count, pending, err = taskListResultTrace(strings.NewReader(open+resumed+late+unknown), "/private/output", roots)
	if err != nil || string(op) != open || string(tail) != late+unknown || count != 1 || pending != 0 {
		t.Fatalf("resumed operation=%q tail=%q unknown=%d pending=%d err=%v", op, tail, count, pending, err)
	}
	annotated := strings.ReplaceAll(open, " = -1 ENOENT (missing)", " = 3</tmp/source>")
	op, _, _, _, err = taskListResultTrace(strings.NewReader(annotated+marker), "/private/output", roots)
	if err != nil || string(op) != annotated {
		t.Fatalf("annotated operation=%q err=%v", op, err)
	}
	for _, trace := range []string{
		open + unknown + marker,
		open + "102 openat(AT_FDCWD, \"/tmp/x\", O_RDONLY <unfinished ...>\n" + marker + "102 <... openat resumed>) = 3</tmp/x>\n",
		open + "101 write(1<pipe:[1]>, \"row\", 3) = 3\n" + unknown,
		open + "101 write(1</wrong/output>, \"row\", 3) = 3\n" + unknown,
		open + "101 write(1</private/output>, \"\", 0) = 0\n" + unknown,
		open + "101 write(1</private/output>, \"row\", 3) = -1 EIO (error)\n" + unknown,
		open + marker + open,
		open + marker + strings.ReplaceAll(open, " = -1 ENOENT (missing)", " = 3</tmp/source>"),
		open + marker + strings.ReplaceAll(open, " = -1 ENOENT (missing)", " <unfinished ...>"),
		open + marker + "102 <... openat resumed>) = 3</tmp/x>\n",
	} {
		if _, _, _, _, err := taskListResultTrace(strings.NewReader(trace), "/private/output", roots); err == nil {
			t.Fatalf("accepted invalid boundary trace %q", trace)
		}
	}
	spaced := strings.ReplaceAll(annotated, "3</tmp/source>", "3</tmp/source with spaces>")
	if got, err := parseTaskCacheLookups(strings.NewReader(spaced), roots); err != nil || !got[root] {
		t.Fatalf("space-annotated source lookup=%v err=%v", got, err)
	}
	device := "101 openat(AT_FDCWD, \"/dev/null\", O_RDONLY) = 3</dev/null<char 1:3>>\n"
	if _, err := parseTaskCacheLookups(strings.NewReader(device), roots); err != nil {
		t.Fatalf("device-annotated open: %v", err)
	}
	for _, value := range []string{"3</tmp/file>", "3</tmp/file with spaces>", "3</dev/null<char 1:3>>", "0</dev/null>", "3", "-1 ENOENT (No such file)"} {
		if _, err := taskTraceFDResult(value); err != nil {
			t.Fatalf("valid FD result %q: %v", value, err)
		}
	}
	for _, value := range []string{"3<>", "-1</tmp/file>", "3</tmp/file", "3</tmp/file>junk", "???", "?"} {
		if _, err := taskTraceFDResult(value); err == nil {
			t.Fatalf("accepted malformed FD result %q", value)
		}
	}
}
