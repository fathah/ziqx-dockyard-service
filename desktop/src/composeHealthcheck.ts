import { isMap, isSeq, parseDocument, visit, type Node } from "yaml";

export type Healthcheck = {
  test: string[];
  interval: string;
  timeout: string;
  retries: number;
  start_period: string;
};

const timing = {
  interval: "1m",
  timeout: "5s",
  retries: 5,
  start_period: "30s",
};

export type HealthcheckService = {
  name: string;
  disabled: boolean;
  interval: string;
  dependents: string[];
};

function healthcheckDocument(source: string) {
  if (new TextEncoder().encode(source).length > 65536)
    throw new Error("Compose must be at most 64 KiB.");
  const doc = parseDocument(source, { merge: true });
  if (doc.errors.length || doc.warnings.length)
    throw new Error(
      "Fix the Compose syntax errors before changing health checks.",
    );
  const data = doc.toJS({ maxAliasCount: 100 });
  if (
    !data?.services ||
    typeof data.services !== "object" ||
    Array.isArray(data.services)
  )
    throw new Error("Add services to the Compose file first.");
  return {
    doc,
    services: data.services as Record<string, Record<string, any>>,
  };
}

function disabledHealthcheck(health: Record<string, any> | undefined): boolean {
  return health?.disable === true || health?.test?.[0] === "NONE";
}

export function healthcheckServices(source: string): HealthcheckService[] {
  const { services } = healthcheckDocument(source);
  return listHealthcheckServices(services);
}

function listHealthcheckServices(
  services: Record<string, Record<string, any>>,
): HealthcheckService[] {
  return Object.entries(services)
    .filter(
      ([, config]) =>
        config && typeof config === "object" && !Array.isArray(config),
    )
    .map(([name, config]) => ({
      name,
      disabled: disabledHealthcheck(config.healthcheck),
      // Docker's normalized JSON expresses durations in nanoseconds.
      interval:
        typeof config.healthcheck?.interval === "number"
          ? `${config.healthcheck.interval / 1e9}s`
          : String(config.healthcheck?.interval ?? ""),
      dependents: Object.entries(services)
        .filter(
          ([, other]) =>
            other?.depends_on?.[name]?.condition === "service_healthy",
        )
        .map(([dependent]) => dependent),
    }));
}

function containsAnchor(node: Node | undefined): boolean {
  let anchored = false;
  if (node)
    visit(node, {
      Value: (_key, value) => {
        if (value.anchor) {
          anchored = true;
          return visit.BREAK;
        }
      },
    });
  return anchored;
}

// Validate a Docker/Go duration without resolving Compose interpolation here.
export function validHealthcheckInterval(interval: string): boolean {
  if (!interval || interval.length > 64) return false;
  const parts = interval.match(/(?:\d+(?:\.\d+)?|\.\d+)(?:ns|us|µs|ms|s|m|h)/g);
  return (
    !!parts &&
    parts.join("") === interval &&
    parts.some((part) => Number.parseFloat(part) > 0)
  );
}

// Edit the same Compose draft as the code editor; deployment applies it.
export function configureHealthcheck(
  source: string,
  service: string,
  settings: { disabled: boolean; interval?: string; waitForStart?: boolean },
): string {
  const { doc, services } = healthcheckDocument(source);
  const selected = listHealthcheckServices(services).find(
    (item) => item.name === service,
  );
  if (!selected)
    throw new Error(`Service ${service} is not defined in Compose.`);
  const node = doc.getIn(["services", service]);
  if (!isMap(node) || node.anchor)
    throw new Error(
      `Edit ${service}'s healthcheck directly in Compose; this service uses a shared YAML anchor.`,
    );
  const existing = node.get("healthcheck", true);
  if (containsAnchor(existing as Node | undefined))
    throw new Error(
      `Edit ${service}'s shared healthcheck anchor directly in Compose.`,
    );
  if (settings.disabled && selected.dependents.length && !settings.waitForStart)
    throw new Error(
      `${selected.dependents.join(", ")} must wait for container startup instead of health before disabling this check.`,
    );
  if (
    !settings.disabled &&
    settings.interval &&
    !validHealthcheckInterval(settings.interval)
  )
    throw new Error("Use a positive interval such as 30s, 1m or 5m.");
  const health = { ...services[service].healthcheck };
  if (settings.disabled) {
    health.disable = true;
  } else {
    delete health.disable;
    if (health.test?.[0] === "NONE") delete health.test;
    if (settings.interval) health.interval = settings.interval;
    else delete health.interval;
  }
  const value = doc.createNode(health);
  if (isMap(value)) {
    const test = value.get("test", true);
    if (isSeq(test)) test.flow = true;
  }
  doc.setIn(["services", service, "healthcheck"], value);
  if (settings.disabled && settings.waitForStart) {
    for (const dependent of selected.dependents) {
      const target = doc.getIn(["services", dependent]);
      if (!isMap(target) || target.anchor)
        throw new Error(
          `Edit ${dependent}'s shared dependency settings directly in Compose.`,
        );
      // Copy resolved dependencies so aliases and merge defaults stay untouched.
      const dependencies = { ...services[dependent].depends_on };
      dependencies[service] = {
        ...dependencies[service],
        condition: "service_started",
      };
      const previous = target.get("depends_on", true);
      if (containsAnchor(previous as Node | undefined))
        throw new Error(
          `Edit ${dependent}'s shared dependency anchor directly in Compose.`,
        );
      target.set("depends_on", doc.createNode(dependencies));
    }
  }
  return doc.toString({ lineWidth: 0, flowCollectionPadding: false });
}

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
  if (doc.errors.length)
    throw new Error("Fix the Compose syntax errors first.");
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
