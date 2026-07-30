import type { KeyboardEvent } from "react";

export function submitFormShortcut(event: KeyboardEvent<HTMLFormElement>) {
  if (event.key !== "Enter" || (!event.ctrlKey && !event.metaKey) || event.repeat) return;
  event.preventDefault();
  event.currentTarget.requestSubmit();
}
