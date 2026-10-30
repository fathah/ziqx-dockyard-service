import { useEffect, useMemo, useState } from "react";
import * as api from "./api";
import { startServiceImagePolling } from "./serviceImagePoller";

export default function useServiceImages(
  project: string,
  release: string,
  enabled: boolean,
  service?: string,
  refresh: number | string = 0,
  snapshot = "",
) {
  const identity = useMemo(
    () => ({}),
    [project, release, enabled, service, refresh, snapshot],
  );
  const [result, setResult] = useState<{
    identity: object;
    task: api.ImageTask;
  }>();
  useEffect(() => {
    if (!enabled || !release) return;
    const visible = () => document.visibilityState !== "hidden";
    const polling = startServiceImagePolling({
      request: (retry, fresh) =>
        api.serviceImages(project, release, service, retry, fresh),
      publish: (task) => setResult({ identity, task }),
      prepare: !!service,
      retryPreparation: !!service && typeof refresh === "number" && refresh > 0,
      visible,
    });
    const resume = () => {
      if (!service && visible()) polling.refresh();
    };
    if (!service) {
      window.addEventListener("focus", resume);
      document.addEventListener("visibilitychange", resume);
    }
    return () => {
      polling.stop();
      window.removeEventListener("focus", resume);
      document.removeEventListener("visibilitychange", resume);
    };
  }, [identity]);
  // Hide a previous project's or deployment's result in the same render that
  // changes its inputs, before the replacement effect runs.
  return enabled && result?.identity === identity ? result.task : undefined;
}
