package notes

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

type answerMatchQueryCost struct {
	cpuNS      int64
	readChars  uint64
	writeChars uint64
	readBytes  uint64
	writeBytes uint64
}

func answerMatchProcessCost(t *testing.T) answerMatchQueryCost {
	t.Helper()
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Fatal(err)
	}
	counters := map[string]uint64{}
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		counters[strings.TrimSuffix(fields[0], ":")] = value
	}
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return answerMatchQueryCost{
		cpuNS:     usage.Utime.Nano() + usage.Stime.Nano(),
		readChars: counters["rchar"], writeChars: counters["wchar"],
		readBytes: counters["read_bytes"], writeBytes: counters["write_bytes"],
	}
}

func TestIndexedAnswerMatchQueryCost(t *testing.T) {
	c, dir, answers := answerMatchCohort(t)
	filter := SearchFilter{Labels: []string{"cohort"}, Limit: 1}
	text := "phrase beacon lexeme200"
	want := matchAnswers(answers, text, filter.Limit, true)
	queries := []struct {
		name   string
		run    func(*Client) ([]AnswerMatch, error)
		bodies int64
	}{
		{"indexed", func(c *Client) ([]AnswerMatch, error) { return c.MatchAnswers(t.Context(), text, filter) }, 1},
		{"reference", func(c *Client) ([]AnswerMatch, error) {
			all, err := c.Answers(t.Context(), DocumentFilter{Labels: filter.Labels})
			if err != nil {
				return nil, err
			}
			return matchAnswers(all, text, filter.Limit, true), nil
		}, int64(len(answers))},
	}
	var allocations [2]float64
	for i, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			got, err := query.run(c)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("warm complete DTO/rank differs from reference: %v", err)
			}
			reads := answerMatchBodyReads(t, c, dir, func(fresh *Client, _ *atomic.Int64) {
				before := answerMatchProcessCost(t)
				got, err := query.run(fresh)
				after := answerMatchProcessCost(t)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("uncached complete DTO/rank differs from reference: %v", err)
				}
				t.Logf("path=%s phase=uncached queries=1 full_snapshot=PASS cpu_ns=%d logical_read_bytes=%d logical_write_bytes=%d physical_read_bytes=%d physical_write_bytes=%d", query.name, after.cpuNS-before.cpuNS, after.readChars-before.readChars, after.writeChars-before.writeChars, after.readBytes-before.readBytes, after.writeBytes-before.writeBytes)
			})
			if reads != query.bodies {
				t.Fatalf("%s read %d bodies; want %d", query.name, reads, query.bodies)
			}
			fresh, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fresh.Close() }()
			if _, err := query.run(fresh); err != nil {
				t.Fatal(err)
			}
			before := answerMatchProcessCost(t)
			allocations[i] = testing.AllocsPerRun(3, func() { got, err = query.run(fresh) })
			after := answerMatchProcessCost(t)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("measured complete DTO/rank differs from reference: %v", err)
			}
			t.Logf("path=%s phase=warm queries=4 full_snapshot=PASS allocs_per_query=%.0f uncached_body_reads=%d corpus=%d cpu_ns=%d logical_read_bytes=%d logical_write_bytes=%d physical_read_bytes=%d physical_write_bytes=%d", query.name, allocations[i], reads, len(answers), after.cpuNS-before.cpuNS, after.readChars-before.readChars, after.writeChars-before.writeChars, after.readBytes-before.readBytes, after.writeBytes-before.writeBytes)
		})
	}
	if allocations[0]*4 > allocations[1]*3 {
		t.Fatalf("full indexed query allocations %.0f exceed 75%% of reference %.0f", allocations[0], allocations[1])
	}
}
