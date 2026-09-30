export interface SetupAdvice {
  title: string;
  message: string;
  action: string;
  command: string | null;
  phase: string | null;
}

export function setupAdvice(error: string): SetupAdvice | null {
  const prefix = "DOCKYARD_SETUP_ERROR:";
  if (!error.startsWith(prefix)) return null;
  try {
    const value = JSON.parse(error.slice(prefix.length));
    if (
      typeof value.title !== "string" ||
      typeof value.message !== "string" ||
      typeof value.action !== "string" ||
      (value.command !== null && typeof value.command !== "string") ||
      (value.phase !== null && typeof value.phase !== "string")
    )
      return null;
    return value;
  } catch {
    return null;
  }
}
