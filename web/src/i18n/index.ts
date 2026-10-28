import i18n, { type BackendModule, type ResourceKey } from "i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import { initReactI18next } from "react-i18next";

import en from "./locales/en.json";

export const SUPPORTED_LANGUAGES = ["en", "zh-CN", "ko"] as const;
export type SupportedLanguage = (typeof SUPPORTED_LANGUAGES)[number];

export const LANGUAGE_STORAGE_KEY = "codex-lb-language";

// Only the English fallback ships in the entry chunk; the other catalogues
// (~100 KB each) are separate chunks fetched when detected or selected.
const resources = {
  en: { translation: en },
} as const;

const LAZY_CATALOGUES: Record<Exclude<SupportedLanguage, "en">, () => Promise<{ default: ResourceKey }>> = {
  "zh-CN": () => import("./locales/zh-CN.json"),
  ko: () => import("./locales/ko.json"),
};

const lazyCatalogueBackend: BackendModule = {
  type: "backend",
  init() {},
  read(language, _namespace, callback) {
    const load = LAZY_CATALOGUES[language as keyof typeof LAZY_CATALOGUES];
    if (!load) {
      callback(null, {});
      return;
    }
    load().then(
      (catalogue) => callback(null, catalogue.default),
      (error: unknown) => callback(error instanceof Error ? error : new Error(String(error)), null),
    );
  },
};

export function normalizeSupportedLanguage(lng: string | null | undefined): SupportedLanguage {
  if (!lng) {
    return "en";
  }
  const exactMatch = SUPPORTED_LANGUAGES.find((supported) => supported.toLowerCase() === lng.toLowerCase());
  if (exactMatch) {
    return exactMatch;
  }
  const baseLanguage = lng.split(/[-_]/, 1)[0]?.toLowerCase();
  if (baseLanguage === "zh") {
    return "zh-CN";
  }
  if (baseLanguage === "en") {
    return "en";
  }
  if (baseLanguage === "ko") {
    return "ko";
  }
  return "en";
}

/**
 * Resolves once the detected language's catalogue is available. The app
 * renders after it so a Korean or Chinese session never flashes English.
 */
export const i18nReady: Promise<unknown> = i18n
  .use(lazyCatalogueBackend)
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    partialBundledLanguages: true,
    supportedLngs: [...SUPPORTED_LANGUAGES],
    fallbackLng: "en",
    load: "currentOnly",
    interpolation: { escapeValue: false },
    detection: {
      order: ["querystring", "localStorage", "navigator"],
      lookupQuerystring: "lang",
      lookupLocalStorage: LANGUAGE_STORAGE_KEY,
      caches: ["localStorage"],
      convertDetectedLanguage: normalizeSupportedLanguage,
    },
    returnNull: false,
  });

function applyHtmlLang(lng: string): void {
  if (typeof document === "undefined") {
    return;
  }
  document.documentElement.lang = lng;
}

applyHtmlLang(normalizeSupportedLanguage(i18n.resolvedLanguage ?? i18n.language));
i18n.on("languageChanged", (lng) => applyHtmlLang(normalizeSupportedLanguage(lng)));

export default i18n;
