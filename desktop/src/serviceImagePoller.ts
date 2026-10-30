import type { ImageTask } from "./api";

type Request = (retry: boolean, refresh: boolean) => Promise<ImageTask>;

// Metadata is rechecked while visible; prepared pulls stay pinned until the
// user retries or the project snapshot changes.
export function startServiceImagePolling({
  request,
  publish,
  prepare = false,
  retryPreparation = false,
  visible = () => true,
  schedule = (callback: () => void, delay: number) =>
    setTimeout(callback, delay),
  cancel = (timer: ReturnType<typeof setTimeout>) => clearTimeout(timer),
  now = Date.now,
}: {
  request: Request;
  publish: (task: ImageTask) => void;
  prepare?: boolean;
  retryPreparation?: boolean;
  visible?: () => boolean;
  schedule?: (
    callback: () => void,
    delay: number,
  ) => ReturnType<typeof setTimeout>;
  cancel?: (timer: ReturnType<typeof setTimeout>) => void;
  now?: () => number;
}) {
  let stopped = false;
  let working = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let deadline = 0;
  let retry = retryPreparation;
  let fresh = false;
  let legacy = false;

  function later(callback: () => void, delay: number) {
    if (timer !== undefined) cancel(timer);
    if (!stopped) timer = schedule(callback, delay);
  }
  function finish(task: ImageTask) {
    if (stopped) return;
    working = false;
    publish(task);
    if (!prepare) {
      const changed =
        task.error?.includes("SERVICE_IMAGE_CHANGED") ||
        task.services?.some(
          (service) => service.error === "SERVICE_IMAGE_CHANGED",
        );
      later(begin, changed ? 3000 : 60_000);
    }
  }
  async function poll() {
    try {
      const result = await request(retry, fresh && !legacy);
      if (stopped) return;
      retry = false;
      fresh = false;
      if (result.status === "checking" || result.status === "pulling") {
        if (now() >= deadline) {
          finish({
            status: "failed",
            error: prepare
              ? "IMAGE_PREPARATION_TIMEOUT"
              : "IMAGE_CHECK_TIMEOUT",
          });
        } else {
          publish(result);
          later(() => void poll(), 1500);
        }
      } else finish(result);
    } catch (error) {
      if (stopped) return;
      const message = String(error);
      // Older agents reject the new metadata refresh flag. Their bounded cache
      // is still re-read on later checks; preparation requests never change.
      if (!prepare && fresh && !legacy && message.includes("REQUEST_INVALID")) {
        legacy = true;
        void poll();
      } else if (message.includes("RATE_LIMITED") && now() < deadline) {
        later(() => void poll(), 3000);
      } else finish({ status: "failed", error: message });
    }
  }
  function begin() {
    if (stopped || working) return;
    if (!prepare && !visible()) {
      later(begin, 60_000);
      return;
    }
    if (timer !== undefined) cancel(timer);
    working = true;
    fresh = !prepare;
    deadline = now() + 11 * 60_000;
    publish({ status: prepare ? "pulling" : "checking" });
    void poll();
  }
  begin();
  return {
    refresh: begin,
    stop() {
      stopped = true;
      if (timer !== undefined) cancel(timer);
    },
  };
}
