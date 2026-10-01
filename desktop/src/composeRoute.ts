import { parseDocument } from "yaml";

export type RouteService = {
  name: string;
  ports: number[];
  unresolved: boolean;
};
export type RouteDetection = { services: RouteService[]; issue?: string };

// Suggestions only: Docker on the VPS remains the authoritative validator.
// Only numeric dotenv values are used; no host environment or file reads.
export function detectComposeRoutes(
  source: string,
  dotenv: string,
): RouteDetection {
  if (new TextEncoder().encode(source).length > 65536)
    return { services: [], issue: "Compose must be at most 64 KiB." };
  const variables = new Map<string, string>();
  for (const line of dotenv.split(/\r?\n/)) {
    const match = line.match(
      /^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$/,
    );
    if (!match) continue;
    const value = match[2].replace(/\s+#.*$/, "");
    variables.set(match[1], value.replace(/^(['"])(\d*)\1$/, "$2"));
  }
  function port(value: unknown): number | undefined {
    let text = String(value ?? "");
    if (text.includes("$$")) return;
    text = text.replace(
      /\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:-|-)(\d+))?\}|\$([A-Za-z_][A-Za-z0-9_]*)/g,
      (original, name, operator, fallback, bare) => {
        const saved = variables.get(name ?? bare);
        if (saved === undefined || (saved === "" && operator === ":-"))
          return fallback ?? original;
        return /^\d+$/.test(saved) ? saved : original;
      },
    );
    if (!/^\d+$/.test(text)) return;
    const number = Number(text);
    return Number.isInteger(number) && number > 0 && number <= 65535
      ? number
      : undefined;
  }
  try {
    const document = parseDocument(source, { merge: true });
    if (document.errors.length || document.warnings.length)
      return {
        services: [],
        issue: "Check the Compose syntax before detecting the app connection.",
      };
    const data = document.toJS({ maxAliasCount: 100 });
    if (
      !data?.services ||
      typeof data.services !== "object" ||
      Array.isArray(data.services)
    )
      return {
        services: [],
        issue: "Add services to the Compose file to detect the app connection.",
      };
    const services: RouteService[] = [];
    for (const [name, service] of Object.entries(data.services)) {
      if (!service || typeof service !== "object" || Array.isArray(service))
        continue;
      const config = service as Record<string, unknown>;
      const ports = new Set<number>();
      let unresolved = false;
      for (const entry of [
        ...(Array.isArray(config.ports) ? config.ports : []),
        ...(Array.isArray(config.expose) ? config.expose : []),
      ]) {
        let target: unknown;
        if (entry && typeof entry === "object") {
          if (entry.protocol && entry.protocol !== "tcp") continue;
          target = entry.target;
        } else {
          const [mapping, protocol = "tcp"] = String(entry).split("/");
          if (protocol !== "tcp") continue;
          // Do not split the colon in ${PORT:-3000} as a host-port delimiter.
          target = mapping
            .replace(/\$\{[^}]*\}/g, (value) => value.replaceAll(":", "\u0001"))
            .split(":")
            .at(-1)
            ?.replaceAll("\u0001", ":");
        }
        const number = port(target);
        if (number !== undefined) ports.add(number);
        else unresolved = true;
      }
      services.push({
        name,
        ports: [...ports].sort((a, b) => a - b),
        unresolved,
      });
    }
    return { services };
  } catch {
    return {
      services: [],
      issue: "Check the Compose syntax before detecting the app connection.",
    };
  }
}

export function suggestComposeRoute(
  detection: RouteDetection,
  savedService?: string,
  savedPort?: number,
) {
  const saved = detection.services.find((s) => s.name === savedService);
  const candidates = detection.services.filter(
    (s) => s.ports.length || s.unresolved,
  );
  const selected =
    saved ??
    (candidates.length === 1
      ? candidates[0]
      : detection.services.length === 1
        ? detection.services[0]
        : undefined);
  const reusePort =
    saved &&
    selected === saved &&
    savedPort &&
    (saved.ports.includes(savedPort) ||
      (!saved.ports.length && !saved.unresolved));
  return {
    service: selected?.name ?? "",
    port: reusePort
      ? String(savedPort)
      : selected?.ports.length === 1 && !selected.unresolved
        ? String(selected.ports[0])
        : "",
  };
}
