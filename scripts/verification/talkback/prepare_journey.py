"""Stage an installed app test that gives the real screen reader focus."""
from argparse import ArgumentParser
from pathlib import Path
from prepare_probe import replace_once


def stage(source, output):
    text = source.read_text()
    text = replace_once(text,
        'group !== "reminders" && group !== "reminder-text")',
        'group !== "reminders" && group !== "reminder-text" && group !== "talkback")')
    text = replace_once(text,
        'reminder-opt-in|reminder-save))$/.test(',
        'reminder-opt-in|reminder-save)|speech-focus:[1-9][0-9]{0,3}:(?:opt-in|save|device-off))$/.test(')
    text = replace_once(text,
        'kind: "activate" | "document" | "accessibility",',
        'kind: "activate" | "document" | "accessibility" | "speech-focus",')
    text = replace_once(text, 'try {\n  await connect();', '''async function talkbackJourney() {
  const service = process.env.MYCHEF_TALKBACK_COMPONENT ?? "";
  if (!/^(?:com[.]android[.]talkback|com[.]google[.]android[.]marvin[.]talkback)\\/[A-Za-z0-9_.]+$/.test(service))
    throw new Error("Expected the installed Google TalkBack service.");
  const checkpoint = async (label: string) => {
    await writeFile(join(state, "talkback-setup-stage"), label);
  };
  await checkpoint("driver-connected");
  await login();
  await checkpoint("signed-in");
  await profile();
  await checkpoint("profile");
  const group = page.locator(".planning-reminders");
  await group.locator("summary").click();
  await checkpoint("panel-open");
  const optIn = group.getByRole("checkbox", { name: "Monday reminder", exact: true });
  const save = button("Save reminder preferences");
  const off = button("Turn off on this device");
  await group.getByLabel("Time zone").fill("Europe/Brussels");
  await checkpoint("time-zone-entered");
  await optIn.check();
  await checkpoint("opt-in-checked");
  await save.click();
  await checkpoint("save-clicked");
  await expect(group.getByRole("status")).toHaveText("Reminder preferences saved.");
  await checkpoint("preferences-saved");
  adb("shell", "pm", "grant", app, "android.permission.POST_NOTIFICATIONS");
  await checkpoint("permission-granted");
  await button("Turn on for this device").click();
  await checkpoint("register-clicked");
  await expect(off).toBeEnabled();
  await checkpoint("device-registered");
  expect(await group.evaluate((element) => {
    const bounds = element.getBoundingClientRect();
    return document.documentElement.scrollWidth <= window.innerWidth + 1 &&
      bounds.left >= 0 && bounds.right <= window.innerWidth + 1 &&
      element.scrollWidth <= element.clientWidth + 1;
  })).toBe(true);
  expect(adb("shell", "settings", "get", "system", "font_scale")).toBe("1.5");
  await checkpoint("large-text-fits");
  adb("shell", "settings", "put", "secure", "enabled_accessibility_services", service);
  adb("shell", "settings", "put", "secure", "accessibility_enabled", "1");
  await checkpoint("talkback-requested");
  const readerPackage = service.split("/")[0];
  await expect.poll(() => {
    const active = adb("shell", "dumpsys", "activity", "services", readerPackage);
    const configured = adb("shell", "settings", "get", "secure", "enabled_accessibility_services");
    return configured === service && active.split("\\n").some(line =>
      /ServiceRecord/.test(line) && line.includes(readerPackage) && line.includes("TalkBackService")
    );
  }, { timeout: 20_000 }).toBe(true);
  await checkpoint("talkback-bound");
  // The first time the service starts, its tutorial can take focus. Return to
  // the existing activity without clearing the signed-in fixture or settings.
  if (!adb("shell", "dumpsys", "window", "displays").split("\\n").some(line => line.includes("mCurrentFocus") && line.includes(app))) {
    adb("shell", "am", "start", "-W", "-n", app + "/.MainActivity");
  }
  await checkpoint("app-foregrounded");
  await writeFile(join(state, "talkback-ready"), "ready\\n");
  await expect.poll(async () =>
    await readFile(join(state, "talkback-capture-ready"), "utf8").catch(() => ""),
    { timeout: 25_000 }).toBe("capturing\\n");
  for (const [target, control] of [
    ["opt-in", optIn], ["save", save], ["device-off", off],
  ] as const) {
    await check("OS accessibility focus with TalkBack on " + target, async () => {
      await control.scrollIntoViewIfNeeded();
      await probeAction("speech-focus", target);
      await writeFile(join(state, "talkback-focused-" + target), "focused\\n");
      await Bun.sleep(4500);
    });
  }
  await off.click();
  await expect(button("Turn on for this device")).toBeEnabled();
}

try {
  await connect();''')
    text = replace_once(text,
        '    "reminder-text": reminderTextJourney,',
        '    "reminder-text": reminderTextJourney,\n    talkback: talkbackJourney,')
    ending = '  await device?.close().catch(() => undefined);\n}\n'
    if not text.endswith(ending):
        raise ValueError("Probe cleanup is no longer last")
    text = text[:-len(ending)] + (
        '''  if (group === "talkback") {
    try {
      adb("shell", "settings", "delete", "secure", "enabled_accessibility_services");
      adb("shell", "settings", "put", "secure", "accessibility_enabled", "0");
    } catch {}
  }
  await device?.close().catch(() => undefined);
}
''')
    output.write_text(text)


if __name__ == "__main__":
    parser = ArgumentParser()
    parser.add_argument("source", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    stage(args.source.resolve(strict=True), args.output.resolve())
