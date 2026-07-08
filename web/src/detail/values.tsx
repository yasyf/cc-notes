// Per-field renderers for a typed trail change value. A scalar field renders
// from → to (time fields as local date-times, null/empty as ∅); a set field
// renders its added/removed elements by the field's element shape — comments and
// log entries as authored blocks, criteria/anchors/attachments/commits/ids as
// chips, other strings as plain chips.

import type { ReactNode } from "react";
import type {
  Anchor,
  Attachment,
  Comment,
  Criterion,
  LogEntry,
  TrailChange,
  TrailValue,
} from "../api";
import { isTimeField, scalarText } from "./format";
import { AttachmentChip } from "./Attachments";
import { AnchorChip, AuthoredBlock, Chip, CommitChip, CriterionChip, IdChip, TimeText } from "./parts";

export function ChangeValue({ change }: { change: TrailChange }) {
  if (change.scalar) {
    return <ScalarDelta field={change.field} from={change.from} to={change.to} />;
  }
  return <SetDelta field={change.field} added={change.added} removed={change.removed} />;
}

function ScalarDelta({ field, from, to }: { field: string; from: TrailValue; to: TrailValue }) {
  return (
    <span className="trail-delta">
      <ScalarValue field={field} value={from} className="trail-from" />
      <span className="trail-arrow" aria-hidden="true">
        →
      </span>
      <ScalarValue field={field} value={to} className="trail-to" />
    </span>
  );
}

function ScalarValue({
  field,
  value,
  className,
}: {
  field: string;
  value: TrailValue;
  className: string;
}) {
  if (isTimeField(field) && typeof value === "number") {
    return (
      <span className={className}>
        <TimeText sec={value} />
      </span>
    );
  }
  return <span className={className}>{scalarText(value)}</span>;
}

function SetDelta({
  field,
  added,
  removed,
}: {
  field: string;
  added: TrailValue[];
  removed: TrailValue[];
}) {
  const authored = field === "comments" || field === "entries";
  return (
    <div className={authored ? "trail-set trail-set-block" : "trail-set"}>
      {added.map((v, i) => (
        <SetElement key={`a${i}`} field={field} value={v} sign="+" />
      ))}
      {removed.map((v, i) => (
        <SetElement key={`r${i}`} field={field} value={v} sign="-" />
      ))}
    </div>
  );
}

function SetElement({
  field,
  value,
  sign,
}: {
  field: string;
  value: TrailValue;
  sign: "+" | "-";
}) {
  switch (field) {
    case "comments": {
      const c = value as unknown as Comment;
      return <AuthoredBlock author={c.author} ts={c.ts} body={c.body} sign={sign} />;
    }
    case "entries": {
      const e = value as unknown as LogEntry;
      return <AuthoredBlock author={e.author} ts={e.ts} body={e.text} sign={sign} />;
    }
    case "criteria":
      return (
        <SignWrap sign={sign}>
          <CriterionChip criterion={value as unknown as Criterion} />
        </SignWrap>
      );
    case "anchors":
      return (
        <SignWrap sign={sign}>
          <AnchorChip anchor={value as unknown as Anchor} />
        </SignWrap>
      );
    case "attachments":
      return (
        <SignWrap sign={sign}>
          <AttachmentChip attachment={value as unknown as Attachment} />
        </SignWrap>
      );
    case "commits":
      return (
        <SignWrap sign={sign}>
          <CommitChip sha={String(value)} />
        </SignWrap>
      );
    case "blocked_by":
    case "superseded_by":
      return (
        <SignWrap sign={sign}>
          <IdChip id={String(value)} />
        </SignWrap>
      );
    default:
      if (value !== null && typeof value === "object") {
        return (
          <SignWrap sign={sign}>
            <Chip className="chip-json">{JSON.stringify(value)}</Chip>
          </SignWrap>
        );
      }
      return (
        <SignWrap sign={sign}>
          <Chip>{scalarText(value)}</Chip>
        </SignWrap>
      );
  }
}

function SignWrap({ sign, children }: { sign: "+" | "-"; children: ReactNode }) {
  return (
    <span className={sign === "+" ? "set-add" : "set-remove"}>
      <span className="set-sign" aria-hidden="true">
        {sign === "+" ? "＋" : "−"}
      </span>
      {children}
    </span>
  );
}
