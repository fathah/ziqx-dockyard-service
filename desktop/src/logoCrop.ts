export function cropGeometry(width: number, height: number, zoom: number) {
  const side = Math.min(width, height) / zoom;
  return {
    sx: (width - side) / 2,
    sy: (height - side) / 2,
    side,
    output: Math.min(900, Math.max(1, Math.floor(side))),
  };
}
