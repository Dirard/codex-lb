import { describe, expect, it } from "vitest";

import en from "@/i18n/locales/en.json";
import ko from "@/i18n/locales/ko.json";
import zhCN from "@/i18n/locales/zh-CN.json";

const placeholders = (value: string) => [...value.matchAll(/\{\{\s*(\w+)\s*\}\}/g)].map((match) => match[1]).sort();

describe("locale catalogues", () => {
  it.each([
    ["ko", ko as Record<string, string>],
    ["zh-CN", zhCN as Record<string, string>],
  ])("%s has every English key with the same placeholders", (_, catalogue) => {
    const english = en as Record<string, string>;
    expect(Object.keys(english).filter((key) => !(key in catalogue))).toEqual([]);
    expect(Object.keys(catalogue).filter((key) => !(key in english))).toEqual([]);
    for (const key of Object.keys(english).filter((k) => k.startsWith("overview.") || k.startsWith("search.") || k.startsWith("requests."))) {
      expect(placeholders(catalogue[key]), key).toEqual(placeholders(english[key]));
    }
  });
});
