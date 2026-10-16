import type { ServerUpdatePreview } from "./api";
import type { Profile } from "./types";

export type ServerUpdateState = {
  preview: ServerUpdatePreview | null;
  busy: boolean;
  error: string;
  attempted: boolean;
  checkedAt: number | null;
};

export function serverUpdateIdentity(profile: Profile | null): string {
  if (!profile) return "";
  return JSON.stringify([
    profile.server_id,
    profile.key_id,
    profile.server_certificate_sha256,
    profile.origin,
    profile.server_ip,
    profile.ssh_port,
    profile.ssh_fingerprint,
  ]);
}

const explain = (error: unknown) =>
  error instanceof Error ? error.message : String(error);

// This cache lives only for the app run. Navigation and locking retain versions;
// a fresh app launch creates a new cache and checks again after unlocking.
export function createServerUpdateCache(
  inspect: () => Promise<ServerUpdatePreview>,
  install: (expected: ServerUpdatePreview) => Promise<string>,
  now: () => number = Date.now,
) {
  function createEntry() {
    let state: ServerUpdateState = {
      preview: null,
      busy: false,
      error: "",
      attempted: false,
      checkedAt: null,
    };
    let generation = 0;
    let pending: Promise<ServerUpdatePreview | null> | null = null;
    let installing = false;
    const listeners = new Set<() => void>();
    function set(patch: Partial<ServerUpdateState>) {
      state = { ...state, ...patch };
      listeners.forEach((listener) => listener());
    }
    async function check(): Promise<ServerUpdatePreview | null> {
      if (pending) return pending;
      if (state.busy || installing) return null;
      const token = generation;
      set({ busy: true, error: "", attempted: true });
      // Deferring the call lets concurrent consumers share the same promise.
      pending = Promise.resolve()
        .then(inspect)
        .then((preview) => {
          if (token !== generation) return null;
          set({ preview, checkedAt: now() });
          return preview;
        })
        .catch((error) => {
          if (token === generation) set({ error: explain(error) });
          return null;
        })
        .finally(() => {
          pending = null;
          if (token === generation) set({ busy: false });
        });
      return pending;
    }
    return {
      subscribe(listener: () => void) {
        listeners.add(listener);
        return () => {
          listeners.delete(listener);
        };
      },
      snapshot: () => state,
      start: () => (state.attempted ? Promise.resolve(null) : check()),
      check,
      pause() {
        generation++;
        // An approved installation can finish after locking. Keep its operation
        // busy and record the verified outcome in this server's entry.
        if (state.busy && !installing)
          set({
            busy: false,
            error: "Version check interrupted. Check again after unlocking.",
          });
      },
      async apply(expected: ServerUpdatePreview): Promise<boolean> {
        if (
          state.busy ||
          pending ||
          installing ||
          !expected.update_available ||
          expected.jobs.active_count > 0
        )
          return false;
        const token = generation;
        installing = true;
        set({ busy: true, error: "" });
        try {
          await install(expected);
          set({
            preview: {
              ...expected,
              installed_dockyard: expected.candidate_dockyard,
              installed_dockyardctl: expected.candidate_dockyardctl,
              installed_version:
                expected.candidate_version === "unversioned"
                  ? null
                  : expected.candidate_version,
              installed_commit:
                expected.candidate_version === "unversioned"
                  ? null
                  : expected.source_commit,
              version_status: "current",
              update_available: false,
            },
            checkedAt: now(),
          });
          return token === generation;
        } catch (error) {
          set({ error: explain(error) });
          return false;
        } finally {
          installing = false;
          set({ busy: false });
        }
      },
    };
  }
  const entries = new Map<string, ReturnType<typeof createEntry>>();
  return {
    entry(identity: string) {
      let entry = entries.get(identity);
      if (!entry) {
        entry = createEntry();
        entries.set(identity, entry);
      }
      return entry;
    },
  };
}

export type ServerUpdateEntry = ReturnType<
  ReturnType<typeof createServerUpdateCache>["entry"]
>;
