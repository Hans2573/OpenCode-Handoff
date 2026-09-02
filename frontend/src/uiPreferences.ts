export type InterfaceDensity = "compact" | "standard";

export const interfaceDensityStorageKey = "agent-handoff:interface-density";
export const defaultInterfaceDensity: InterfaceDensity = "standard";

export function loadInterfaceDensity(): InterfaceDensity {
  try {
    const saved = window.localStorage.getItem(interfaceDensityStorageKey);
    return saved === "compact" || saved === "standard" ? saved : defaultInterfaceDensity;
  } catch {
    return defaultInterfaceDensity;
  }
}

export function applyInterfaceDensity(density: InterfaceDensity): void {
  document.documentElement.dataset.density = density;
}

export function saveInterfaceDensity(density: InterfaceDensity): void {
  applyInterfaceDensity(density);
  try {
    window.localStorage.setItem(interfaceDensityStorageKey, density);
  } catch {
    // UI preferences are best-effort only.
  }
}
