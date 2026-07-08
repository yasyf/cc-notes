// The full current state of the selected entity, rendered per kind. The
// snapshot is canonical model JSON with no intrinsic tag, so it is discriminated
// by the summary.kind the panel already holds: note/doc bodies with lifecycle
// banners, a log's entries, a task's description/criteria/comments/links, and a
// sprint's or project's description/dates/comments.

import type { ReactNode } from "react";
import type {
  DocSnapshot,
  LogSnapshot,
  NoteSnapshot,
  ProjectSnapshot,
  Snapshot,
  SprintSnapshot,
  TaskSnapshot,
} from "../api";
import { Attachments } from "./Attachments";
import { Markdown } from "./Markdown";
import { AuthoredBlock, Chip, CommitChip, IdChip, StatusBadge, TimeText } from "./parts";

export function SnapshotView({ kind, snapshot }: { kind: string; snapshot: Snapshot }) {
  switch (kind) {
    case "note":
    case "doc":
      return <NoteDocView snap={snapshot as NoteSnapshot | DocSnapshot} kind={kind} />;
    case "log":
      return <LogView snap={snapshot as LogSnapshot} />;
    case "task":
      return <TaskView snap={snapshot as TaskSnapshot} />;
    case "sprint":
      return <SprintView snap={snapshot as SprintSnapshot} />;
    case "project":
      return <ProjectView snap={snapshot as ProjectSnapshot} />;
    default:
      return null;
  }
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="snap-section">
      <h4 className="snap-head">{title}</h4>
      {children}
    </section>
  );
}

function ChipRow({ children }: { children: ReactNode }) {
  return <div className="chip-row">{children}</div>;
}

function Field({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="snap-field">
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

function Banner({ tone, children }: { tone: string; children: ReactNode }) {
  return <div className={`banner banner-${tone}`}>{children}</div>;
}

function TagsSection({ tags }: { tags: string[] }) {
  if (tags.length === 0) return null;
  return (
    <Section title="Tags">
      <ChipRow>
        {tags.map((t) => (
          <Chip key={t} className="chip-tag">
            {t}
          </Chip>
        ))}
      </ChipRow>
    </Section>
  );
}

function CommentsSection({ comments }: { comments: { author: string; ts: number; body: string }[] }) {
  if (comments.length === 0) return null;
  return (
    <Section title="Comments">
      <div className="authored-list">
        {comments.map((c, i) => (
          <AuthoredBlock key={i} author={c.author} ts={c.ts} body={c.body} />
        ))}
      </div>
    </Section>
  );
}

function CommitsSection({ commits }: { commits: string[] }) {
  if (commits.length === 0) return null;
  return (
    <Section title="Commits">
      <ChipRow>
        {commits.map((s) => (
          <CommitChip key={s} sha={s} />
        ))}
      </ChipRow>
    </Section>
  );
}

function NoteDocView({ snap, kind }: { snap: NoteSnapshot | DocSnapshot; kind: string }) {
  return (
    <>
      {snap.verified_at > 0 && (
        <Banner tone="good">
          Verified <TimeText sec={snap.verified_at} />
          {snap.verified_by !== "" && <> by {snap.verified_by}</>}
          {snap.verified_commit !== "" && <CommitChip sha={snap.verified_commit} />}
        </Banner>
      )}
      {snap.stale_at > 0 && (
        <Banner tone="warn">
          Stale since <TimeText sec={snap.stale_at} />
          {snap.stale_reason !== "" && <> — {snap.stale_reason}</>}
          {snap.stale_by !== "" && <> ({snap.stale_by})</>}
        </Banner>
      )}
      {snap.superseded_by.length > 0 && (
        <Banner tone="muted">
          Superseded by{" "}
          {snap.superseded_by.map((id) => (
            <IdChip key={id} id={id} />
          ))}
        </Banner>
      )}
      {kind === "doc" && (snap as DocSnapshot).when !== "" && (
        <Section title="When">
          <p className="snap-text">{(snap as DocSnapshot).when}</p>
        </Section>
      )}
      {snap.body.trim() !== "" && (
        <Section title="Body">
          <Markdown>{snap.body}</Markdown>
        </Section>
      )}
      <TagsSection tags={snap.tags} />
      {snap.anchors.length > 0 && (
        <Section title="Anchors">
          <ChipRow>
            {snap.anchors.map((a, i) => (
              <span key={i} className="chip chip-anchor">
                <span className="chip-key">{a.kind}</span>
                {a.value}
              </span>
            ))}
          </ChipRow>
        </Section>
      )}
      {snap.attachments !== undefined && snap.attachments.length > 0 && (
        <Section title="Attachments">
          <Attachments items={snap.attachments} />
        </Section>
      )}
    </>
  );
}

function LogView({ snap }: { snap: LogSnapshot }) {
  return (
    <>
      {snap.entries.length > 0 && (
        <Section title="Entries">
          <div className="authored-list">
            {snap.entries.map((e, i) => (
              <AuthoredBlock key={i} author={e.author} ts={e.ts} body={e.text} />
            ))}
          </div>
        </Section>
      )}
      <TagsSection tags={snap.tags} />
      {snap.attachments !== undefined && snap.attachments.length > 0 && (
        <Section title="Attachments">
          <Attachments items={snap.attachments} />
        </Section>
      )}
    </>
  );
}

function TaskView({ snap }: { snap: TaskSnapshot }) {
  return (
    <>
      {snap.description.trim() !== "" && (
        <Section title="Description">
          <Markdown>{snap.description}</Markdown>
        </Section>
      )}
      <Section title="Details">
        <dl className="snap-fields">
          {snap.type !== "" && <Field label="type" value={snap.type} />}
          <Field label="priority" value={String(snap.priority)} />
          {snap.parent !== "" && <Field label="parent" value={<IdChip id={snap.parent} />} />}
          {snap.sprint !== "" && <Field label="sprint" value={<IdChip id={snap.sprint} />} />}
          {snap.project !== "" && <Field label="project" value={<IdChip id={snap.project} />} />}
          {snap.started_at > 0 && <Field label="started" value={<TimeText sec={snap.started_at} />} />}
          {snap.closed_at > 0 && <Field label="closed" value={<TimeText sec={snap.closed_at} />} />}
        </dl>
      </Section>
      {snap.criteria.length > 0 && (
        <Section title="Criteria">
          <ul className="crit-list">
            {snap.criteria.map((c) => (
              <li key={c.id} className="crit-item">
                <StatusBadge status={c.status} />
                <span className="crit-text" title={c.script || undefined}>
                  {c.text}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}
      <CommentsSection comments={snap.comments} />
      {snap.labels.length > 0 && (
        <Section title="Labels">
          <ChipRow>
            {snap.labels.map((l) => (
              <Chip key={l} className="chip-tag">
                {l}
              </Chip>
            ))}
          </ChipRow>
        </Section>
      )}
      {snap.blocked_by.length > 0 && (
        <Section title="Blocked by">
          <ChipRow>
            {snap.blocked_by.map((id) => (
              <IdChip key={id} id={id} />
            ))}
          </ChipRow>
        </Section>
      )}
      <CommitsSection commits={snap.commits} />
    </>
  );
}

function SprintView({ snap }: { snap: SprintSnapshot }) {
  return (
    <>
      {snap.description.trim() !== "" && (
        <Section title="Description">
          <Markdown>{snap.description}</Markdown>
        </Section>
      )}
      {(snap.project !== "" || snap.start_date > 0 || snap.end_date > 0) && (
        <Section title="Details">
          <dl className="snap-fields">
            {snap.project !== "" && <Field label="project" value={<IdChip id={snap.project} />} />}
            {snap.start_date > 0 && <Field label="start" value={<TimeText sec={snap.start_date} />} />}
            {snap.end_date > 0 && <Field label="end" value={<TimeText sec={snap.end_date} />} />}
          </dl>
        </Section>
      )}
      <CommentsSection comments={snap.comments} />
      {snap.labels.length > 0 && (
        <Section title="Labels">
          <ChipRow>
            {snap.labels.map((l) => (
              <Chip key={l} className="chip-tag">
                {l}
              </Chip>
            ))}
          </ChipRow>
        </Section>
      )}
      <CommitsSection commits={snap.commits} />
    </>
  );
}

function ProjectView({ snap }: { snap: ProjectSnapshot }) {
  return (
    <>
      {snap.description.trim() !== "" && (
        <Section title="Description">
          <Markdown>{snap.description}</Markdown>
        </Section>
      )}
      {snap.closed_at > 0 && (
        <Section title="Details">
          <dl className="snap-fields">
            <Field label="closed" value={<TimeText sec={snap.closed_at} />} />
          </dl>
        </Section>
      )}
      <CommentsSection comments={snap.comments} />
      {snap.labels.length > 0 && (
        <Section title="Labels">
          <ChipRow>
            {snap.labels.map((l) => (
              <Chip key={l} className="chip-tag">
                {l}
              </Chip>
            ))}
          </ChipRow>
        </Section>
      )}
      <CommitsSection commits={snap.commits} />
    </>
  );
}
