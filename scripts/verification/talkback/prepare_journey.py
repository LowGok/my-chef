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
  const group = await openReminderSettings();
  const optIn = group.getByRole("checkbox", { name: "Monday reminder", exact: true });
  const save = button("Save reminder preferences");
  const off = button("Turn off on this device");
  await group.getByLabel("Time zone").fill("Europe/Brussels");
  await optIn.check();
  await save.click();
  await expect(group.getByRole("status")).toHaveText("Reminder preferences saved.");
  adb("shell", "pm", "grant", app, "android.permission.POST_NOTIFICATIONS");
  await button("Turn on for this device").click();
  await expect(off).toBeEnabled();
  expect(await group.evaluate((element) => {
    const bounds = element.getBoundingClientRect();
    return document.documentElement.scrollWidth <= window.innerWidth + 1 &&
      bounds.left >= 0 && bounds.right <= window.innerWidth + 1 &&
      element.scrollWidth <= element.clientWidth + 1;
  })).toBe(true);
  expect(adb("shell", "settings", "get", "system", "font_scale")).toBe("1.5");
  adb("shell", "settings", "put", "secure", "enabled_accessibility_services", service);
  adb("shell", "settings", "put", "secure", "accessibility_enabled", "1");
  await expect.poll(() =>
    adb("shell", "dumpsys", "accessibility").split("\\n").some(line =>
      /bound\\s*services/i.test(line) && line.includes(service.split("/")[0])
    ), { timeout: 15_000 }).toBe(true);
  // The first time the service starts, its tutorial can take focus. Return to
  // the existing activity without clearing the signed-in fixture or settings.
  if (!adb("shell", "dumpsys", "window", "displays").split("\\n").some(line => line.includes("mCurrentFocus") && line.includes(app))) {
    adb("shell", "am", "start", "-W", "-n", app + "/.MainActivity");
  }
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
