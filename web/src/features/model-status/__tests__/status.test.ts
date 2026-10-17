/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { describe, expect, it } from "vitest";

import { parseHeaderNavModules as parseSettings, serializeHeaderNavModules } from "@/features/system-settings/maintenance/config";

import { parseHeaderNavModules } from "@/lib/nav-modules";

describe("status header nav module", () => {
  it("parses a disabled status module", () => {
    const modules = parseHeaderNavModules(
      '{"status":{"enabled":false,"requireAuth":false}}',
    );
    expect(modules.status.enabled).toBe(false);
  });

  it("defaults to enabled without auth when the key is absent", () => {
    const modules = parseHeaderNavModules('{"home":true}');
    expect(modules.status).toEqual({ enabled: true, requireAuth: false });
  });
});


describe('legacy status navigation compatibility', () => {
  it.each([true, false])('preserves the legacy enabled value %s through settings saves', (enabled) => {
    const raw = JSON.stringify({ status: enabled })
    const settings = parseSettings(raw)
    expect(settings.status).toEqual({ enabled, requireAuth: false })
    expect(parseHeaderNavModules(serializeHeaderNavModules(settings)).status).toEqual(settings.status)
  })

  it('preserves the model status login requirement', () => {
    const settings = parseSettings('{"status":{"enabled":true,"requireAuth":true}}')
    expect(parseHeaderNavModules(serializeHeaderNavModules(settings)).status).toEqual({ enabled: true, requireAuth: true })
  })
})
