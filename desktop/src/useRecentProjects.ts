import { useEffect, useState } from "react";

const storageKey = "dockyard.recent-projects.v1";
type History = Record<string, string[]>;

function readHistory(): History {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(storageKey) ?? "{}");
    if (!value || typeof value !== "object" || Array.isArray(value)) return {};
    return Object.fromEntries(
      Object.entries(value).filter(
        ([, ids]) =>
          Array.isArray(ids) && ids.every((id) => typeof id === "string"),
      ),
    );
  } catch {
    return {};
  }
}

export default function useRecentProjects(
  scope: string,
  selected: string | null,
) {
  const [history, setHistory] = useState<History>(readHistory);

  useEffect(() => {
    if (!selected) return;
    setHistory((previous) => {
      const ids = previous[scope] ?? [];
      if (ids[0] === selected) return previous;
      return {
        ...previous,
        [scope]: [selected, ...ids.filter((id) => id !== selected)],
      };
    });
  }, [scope, selected]);

  useEffect(() => {
    try {
      localStorage.setItem(storageKey, JSON.stringify(history));
    } catch {
      // Keep ordering for this session if storage is unavailable.
    }
  }, [history]);

  return history[scope] ?? [];
}
