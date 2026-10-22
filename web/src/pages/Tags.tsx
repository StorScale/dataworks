import { useState } from "react";
import { getTags, makeTag, removeTag } from "../api";
import { Empty, Intro } from "../components";
import { ErrorBanner, Spinner, usePlatform, usePoll, useProject } from "../lib";

// Tags are moments of the project's tables kept (a month-end close, a
// delivery): every table as it was then, readable as long as the tag is
// kept, whatever history is kept otherwise. The project's editors make them.
export default function Tags() {
  const { name, view } = useProject();
  const { admin } = usePlatform();
  const { data, error, reload } = usePoll(() => getTags(name), [name], 30_000);
  const [form, setForm] = useState({ name: "", at: "now", keep: "7y", note: "" });
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string>();
  const canEdit = view?.role === "editor" || admin;
  const act = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setActionError(undefined);
    try { await f(); reload(); } catch (e) { setActionError((e as Error).message); } finally { setBusy(false); }
  };
  if (error && !data) return <div className="page"><ErrorBanner error={error} /></div>;
  if (!data) return <div className="page"><Spinner /></div>;
  return (
    <div className="page stack" data-testid="tags">
      <Intro>
        A tag keeps every table of the project as it was at a moment, for as long as you say: a month-end close, a delivery.
        Read a table at a tag on the SQL page (As of), or from its History.
      </Intro>
      <ErrorBanner error={actionError} />
      {data.tags.length === 0 ? <Empty>No tags yet.</Empty> : (
        <div className="card flush scroll-x"><table className="t" data-testid="tag-list">
          <thead><tr><th>Tag</th><th>At</th><th>Kept until</th><th>Note</th><th>By</th><th></th></tr></thead>
          <tbody>
            {data.tags.map((t) => (
              <tr key={t.name}>
                <td className="mono">{t.name}{!t.inPlace && <div className="small muted">being applied</div>}</td>
                <td className="mono small">{t.commit.slice(0, 12)}</td>
                <td className="small">{t.keepUntil}</td>
                <td className="small">{t.note}</td>
                <td className="small">{t.by}</td>
                <td>{canEdit && <button className="btn small danger" disabled={busy} onClick={() => act(() => removeTag(name, t.name))}>Remove</button>}</td>
              </tr>
            ))}
          </tbody>
        </table></div>
      )}
      {canEdit && (
        <form className="card stack" onSubmit={(e) => { e.preventDefault(); act(() => makeTag(name, form)); }} data-testid="make-tag">
          <h2>Make a tag</h2>
          <div className="fields">
            <label>Name<input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="close-2026-09" required /></label>
            <label>At<input value={form.at} onChange={(e) => setForm({ ...form, at: e.target.value })} placeholder="now, 2026-09-30, a commit" /></label>
            <label>Keep for<input value={form.keep} onChange={(e) => setForm({ ...form, keep: e.target.value })} placeholder="7y or 90d" style={{ width: 110 }} /></label>
            <label className="grow">Note<input value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} placeholder="September's close, signed off by Finance" /></label>
          </div>
          <div className="form-actions"><button className="btn primary" type="submit" disabled={busy || !form.name}>Make the tag</button></div>
        </form>
      )}
    </div>
  );
}
