import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useSyncExternalStore,
} from "react";
import * as api from "./api";
import type { Profile } from "./types";
import {
  createServerUpdateCache,
  serverUpdateIdentity,
  type ServerUpdateEntry,
} from "./serverUpdateCache";

const cache = createServerUpdateCache(
  api.checkServerUpdate,
  api.applyServerUpdate,
);
const standalone = cache.entry("standalone");

function useController(entry: ServerUpdateEntry, enabled: boolean) {
  const state = useSyncExternalStore(entry.subscribe, entry.snapshot);
  return useMemo(
    () => ({
      ...state,
      check: () => (enabled ? entry.check() : Promise.resolve(null)),
      apply: (expected: api.ServerUpdatePreview) =>
        enabled ? entry.apply(expected) : Promise.resolve(false),
    }),
    [entry, enabled, state],
  );
}

type Controller = ReturnType<typeof useController>;
const Context = createContext<Controller | null>(null);
export const ServerUpdatesProvider = Context.Provider;

export function useServerUpdateSession(
  profile: Profile | null,
  enabled: boolean,
) {
  const identity = serverUpdateIdentity(profile);
  const entry = useMemo(() => cache.entry(identity), [identity]);
  const previous = useRef(entry);
  useEffect(() => {
    if (previous.current !== entry) previous.current.pause();
    previous.current = entry;
    if (enabled && identity) void entry.start();
    else entry.pause();
    // There is intentionally no unmount cleanup: StrictMode replays effects,
    // while the app's locked/identity state explicitly invalidates old requests.
  }, [entry, identity, enabled]);
  return useController(entry, enabled);
}

export function useServerUpdates() {
  const shared = useContext(Context);
  const fallback = useController(standalone, true);
  return shared ?? fallback;
}
