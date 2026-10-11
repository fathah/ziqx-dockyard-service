import { createContext, useContext, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Camera, X } from "lucide-react";
import { invoke } from "@tauri-apps/api/core";
import Button from "./Button";
import { native } from "./api";
import { cropGeometry } from "./logoCrop";
import "./ProjectLogo.css";

export const ProjectLogoScope = createContext("preview");
const cache = new Map<string, string | null>();
const listeners = new Set<() => void>();
let dbPromise: Promise<IDBDatabase> | undefined;
function database() {
  return (dbPromise ??= new Promise((resolve, reject) => {
    const request = indexedDB.open("dockyard-project-logos", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("logos");
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => {
      dbPromise = undefined;
      reject(request.error);
    };
  }));
}
async function browserStore(
  key: string,
  value?: string | null,
): Promise<string | null> {
  const db = await database();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(
      "logos",
      value === undefined ? "readonly" : "readwrite",
    );
    const store = tx.objectStore("logos");
    const request =
      value === undefined
        ? store.get(key)
        : value === null
          ? store.delete(key)
          : store.put(value, key);
    tx.oncomplete = () =>
      resolve(value === undefined ? (request.result ?? null) : value);
    tx.onerror = () => reject(tx.error);
    tx.onabort = () => reject(tx.error);
  });
}
function useLogo(project: string) {
  const scope = useContext(ProjectLogoScope);
  const key = JSON.stringify([scope, project]);
  const [, changed] = useState(0);
  useEffect(() => {
    let active = true;
    const notify = () => {
      if (active) changed((v) => v + 1);
    };
    listeners.add(notify);
    if (!cache.has(key)) {
      (native
        ? invoke<string | null>("project_logo_read", { scope, project })
        : browserStore(key)
      )
        .then((value) => {
          if (!cache.has(key)) cache.set(key, value);
          notify();
        })
        .catch(() => {
          // Logos are optional. Missing or unreadable local storage uses initials.
          if (!cache.has(key)) cache.set(key, null);
          notify();
        });
    }
    return () => {
      active = false;
      listeners.delete(notify);
    };
  }, [key, scope, project]);
  return {
    logo: cache.get(key) ?? null,
    save: async (data: string | null) => {
      if (native) await invoke("project_logo_save", { scope, project, data });
      else await browserStore(key, data);
      cache.set(key, data);
      listeners.forEach((notify) => notify());
    },
  };
}
export default function ProjectLogo({
  project,
  name,
  editable = false,
  tone,
}: {
  project: string;
  name: string;
  editable?: boolean;
  tone?: string;
}) {
  const { logo, save } = useLogo(project);
  const [editing, setEditing] = useState(false);
  const [failedLogo, setFailedLogo] = useState<string | null>(null);
  const initials = name
    .split(/[-_\s]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((s) => s[0])
    .join("")
    .toUpperCase();
  const content = (
    <>
      <span
        className={`app-icon project-logo ${tone ?? (name.charCodeAt(0) % 3 === 0 ? "lavender" : name.charCodeAt(0) % 3 === 1 ? "sand" : "mint")}`}
      >
        {logo && logo !== failedLogo ? (
          <img src={logo} alt="" onError={() => setFailedLogo(logo)} />
        ) : (
          initials
        )}
      </span>
      {editable && (
        <span className="project-logo-overlay">
          <Camera size={18} />
          <span>Change logo</span>
        </span>
      )}
    </>
  );
  return (
    <>
      {editable ? (
        <button
          type="button"
          className="project-logo-edit"
          aria-label={`Change logo for ${name}`}
          title="Change project logo"
          onClick={() => setEditing(true)}
        >
          {content}
        </button>
      ) : (
        content
      )}
      {editing && (
        <LogoEditor
          name={name}
          current={logo}
          save={save}
          close={() => setEditing(false)}
        />
      )}
    </>
  );
}
function LogoEditor({
  name,
  current,
  save,
  close,
}: {
  name: string;
  current: string | null;
  save: (data: string | null) => Promise<void>;
  close: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const image = useRef<HTMLImageElement | null>(null);
  const objectURL = useRef("");
  const request = useRef(0);
  const [loaded, setLoaded] = useState(false);
  const [zoom, setZoom] = useState(1);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    dialog.current?.showModal();
    return () => {
      request.current++;
      URL.revokeObjectURL(objectURL.current);
    };
  }, []);
  useEffect(() => {
    const img = image.current;
    const ctx = canvas.current?.getContext("2d");
    if (!img || !ctx || !loaded) return;
    const crop = cropGeometry(img.naturalWidth, img.naturalHeight, zoom);
    ctx.clearRect(0, 0, 300, 300);
    ctx.drawImage(img, crop.sx, crop.sy, crop.side, crop.side, 0, 0, 300, 300);
  }, [loaded, zoom]);
  async function choose(file?: File) {
    if (!file) return;
    const id = ++request.current;
    setError("");
    setLoaded(false);
    if (file.size > 25_000_000) {
      setError("Choose an image smaller than 25 MB.");
      return;
    }
    if (
      ![
        "image/png",
        "image/jpeg",
        "image/webp",
        "image/gif",
        "image/avif",
      ].includes(file.type)
    ) {
      setError("Choose a PNG, JPEG, WebP, GIF or AVIF image.");
      return;
    }
    URL.revokeObjectURL(objectURL.current);
    objectURL.current = URL.createObjectURL(file);
    const img = new Image();
    img.src = objectURL.current;
    try {
      await img.decode();
      if (id !== request.current) return;
      if (
        !img.naturalWidth ||
        !img.naturalHeight ||
        img.naturalWidth * img.naturalHeight > 60_000_000
      )
        throw new Error("Image is too large or couldn't be read.");
      image.current = img;
      setZoom(1);
      setLoaded(true);
    } catch {
      if (id === request.current)
        setError("Couldn't read this image. Try a smaller PNG or JPEG.");
    }
  }
  async function commit(remove = false) {
    setBusy(true);
    setError("");
    try {
      let data: string | null = null;
      if (!remove) {
        const img = image.current;
        if (!img || !loaded) throw new Error("Choose an image first.");
        const crop = cropGeometry(img.naturalWidth, img.naturalHeight, zoom);
        const out = document.createElement("canvas");
        out.width = out.height = crop.output;
        const ctx = out.getContext("2d");
        if (!ctx) throw new Error("Image processing is unavailable.");
        ctx.imageSmoothingQuality = "high";
        ctx.drawImage(
          img,
          crop.sx,
          crop.sy,
          crop.side,
          crop.side,
          0,
          0,
          out.width,
          out.height,
        );
        data = out.toDataURL("image/webp", 0.85);
        if (
          !data.startsWith("data:image/webp;") &&
          !data.startsWith("data:image/png;")
        )
          throw new Error("Image compression is unavailable in this browser.");
      }
      await save(data);
      close();
    } catch (e) {
      setError(String(e).replace(/^Error: /, ""));
    } finally {
      setBusy(false);
    }
  }
  return createPortal(
    <dialog
      ref={dialog}
      className="modal project-logo-modal"
      aria-labelledby="project-logo-title"
      onCancel={(e) => {
        e.preventDefault();
        if (!busy) close();
      }}
    >
      <div className="modal-heading">
        <div>
          <h2 id="project-logo-title">Project logo</h2>
          <p>{name}</p>
        </div>
        <Button
          type="button"
          className="icon-button"
          aria-label="Close logo editor"
          disabled={busy}
          onClick={close}
        >
          <X size={20} />
        </Button>
      </div>
      <p>
        Crop to a square. Saved locally as a compressed image, up to 900 × 900
        pixels.
      </p>
      <input
        ref={input}
        type="file"
        accept="image/png,image/jpeg,image/webp,image/gif,image/avif"
        hidden
        disabled={busy}
        onChange={(e) => {
          void choose(e.target.files?.[0]);
          e.target.value = "";
        }}
      />
      <div className="logo-crop-frame">
        {loaded ? (
          <canvas
            ref={canvas}
            width={300}
            height={300}
            aria-label="Square logo crop preview"
          />
        ) : current ? (
          <img src={current} alt="Current project logo" />
        ) : (
          <Camera size={48} />
        )}
      </div>
      <Button
        type="button"
        className="button"
        disabled={busy}
        onClick={() => input.current?.click()}
      >
        {loaded || current ? "Choose another image" : "Choose image"}
      </Button>
      {loaded && (
        <div className="logo-crop-controls">
          <label>
            Zoom
            <input
              type="range"
              min="1"
              max="4"
              step="0.01"
              value={zoom}
              disabled={busy}
              onChange={(e) => setZoom(Number(e.target.value))}
            />
          </label>
        </div>
      )}
      {error && (
        <p className="alert error" role="alert">
          {error}
        </p>
      )}
      <div className="modal-footer">
        {current && (
          <Button
            type="button"
            className="button"
            disabled={busy}
            onClick={() => void commit(true)}
          >
            Remove logo
          </Button>
        )}
        <Button
          type="button"
          className="button"
          disabled={busy}
          onClick={close}
        >
          Cancel
        </Button>
        <Button
          type="button"
          className="button primary"
          disabled={busy || !loaded}
          onClick={() => void commit()}
        >
          {busy ? "Saving…" : "Save square logo"}
        </Button>
      </div>
    </dialog>,
    document.body,
  );
}
