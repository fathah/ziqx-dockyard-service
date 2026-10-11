import { useEffect, useState } from "react";
import * as api from "./api";

// Poll only while the server's bounded background task is active. Closing the
// view stops polling; a pull may finish and be reused on reopening the dialog.
export default function useServiceImages(
  project: string,
  release: string,
  enabled: boolean,
  service?: string,
  refresh: number | string = 0,
  snapshot = "",
) {
  const [task, setTask] = useState<api.ImageTask>();
  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    setTask(undefined);
    if (!enabled || !release) return;
    const started = Date.now();
    let retry = !!service && typeof refresh === "number" && refresh > 0;
    async function poll() {
      try {
        const result = await api.serviceImages(
          project,
          release,
          service,
          retry,
        );
        retry = false;
        if (cancelled) return;
        setTask(result);
        if (result.status === "checking" || result.status === "pulling") {
          if (Date.now() - started > 11 * 60 * 1000) {
            setTask({ status: "failed", error: "IMAGE_PREPARATION_TIMEOUT" });
          } else timer = setTimeout(() => void poll(), 1500);
        }
      } catch (e) {
        if (cancelled) return;
        // A concurrent project check/pull may briefly fill the background slots.
        if (
          String(e).includes("RATE_LIMITED") &&
          Date.now() - started < 11 * 60 * 1000
        ) {
          timer = setTimeout(() => void poll(), 3000);
        } else setTask({ status: "failed", error: String(e) });
      }
    }
    void poll();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [project, release, enabled, service, refresh, snapshot]);
  return task;
}
