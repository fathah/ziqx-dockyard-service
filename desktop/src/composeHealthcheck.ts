import { isMap, isSeq, parseDocument } from "yaml";

export type Healthcheck = {
  test: string[];
  interval: string;
  timeout: string;
  retries: number;
  start_period: string;
};

const timing = {
  interval: "10s",
  timeout: "5s",
  retries: 5,
  start_period: "30s",
};

// Image name without registry, tag or digest: "ghcr.io/x/postgres:16" -> "postgres".
function imageName(image: string): string {
  const path = image.split("@")[0].split("/").pop() ?? "";
  return path.split(":")[0].toLowerCase();
}

// Pick a probe that ships inside common images. `$$` defers variable
// expansion to the container, not Compose interpolation.
export function suggestHealthcheck(
  image: string,
  port?: number,
  path = "/",
): Healthcheck | undefined {
  const name = imageName(image);
  const shell = (command: string) => ({
    test: ["CMD-SHELL", command],
    ...timing,
  });
  if (/^(postgres|postgis|timescaledb|pgvector)/.test(name))
    return shell(
      'pg_isready -U "$${POSTGRES_USER:-postgres}" -d "$${POSTGRES_DB:-$${POSTGRES_USER:-postgres}}"',
    );
  if (/^(redis|valkey|keydb)/.test(name))
    return shell("redis-cli ping || valkey-cli ping");
  if (/^mariadb/.test(name))
    return shell("healthcheck.sh --connect --innodb_initialized");
  if (/^(mysql|percona)/.test(name))
    return shell("mysqladmin ping -h 127.0.0.1 --silent");
  if (/^mongo/.test(name))
    return shell("mongosh --quiet --eval \"db.adminCommand('ping')\"");
  if (/^rabbitmq/.test(name)) return shell("rabbitmq-diagnostics -q ping");
  if (!port) return undefined;
  // Any HTTP response proves the app is listening; curl and BusyBox wget cover
  // most Debian, Alpine and Node images.
  const url = `http://127.0.0.1:${port}${path.startsWith("/") ? path : "/"}`;
  return shell(
    `curl -s -o /dev/null ${url} || wget -q -O /dev/null ${url} || exit 1`,
  );
}

// Insert into the editor source, preserving comments and formatting.
export function addHealthcheck(
  source: string,
  service: string,
  healthcheck: Healthcheck,
): string {
  const doc = parseDocument(source);
  if (doc.errors.length) throw new Error("Fix the Compose syntax errors first.");
  const node = doc.getIn(["services", service]);
  if (!isMap(node))
    throw new Error(
      `Service ${service} is not defined directly in the saved Compose file.`,
    );
  const existing = node.get("healthcheck", true);
  if (existing) {
    const current = doc.getIn(["services", service, "healthcheck"]);
    const test = isMap(current) ? current.toJSON().test : undefined;
    const disabled =
      isMap(current) &&
      (current.get("disable") === true ||
        (Array.isArray(test) && test[0] === "NONE"));
    if (!disabled)
      throw new Error(`Service ${service} already has a healthcheck.`);
  }
  const value = doc.createNode(healthcheck);
  if (isMap(value)) {
    const test = value.get("test", true);
    if (isSeq(test)) test.flow = true;
  }
  doc.setIn(["services", service, "healthcheck"], value);
  return doc.toString({ lineWidth: 0, flowCollectionPadding: false });
}
