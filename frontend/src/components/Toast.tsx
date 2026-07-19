export type ToastMessage = { message: string; type: "success" | "error" };

export function Toast({ toast }: { toast?: ToastMessage }) {
  if (!toast) return null;
  return (
    <div
      className={`toast${toast.type === "error" ? " error" : ""}`}
      role={toast.type === "error" ? "alert" : "status"}
      aria-live={toast.type === "error" ? "assertive" : "polite"}
    >
      {toast.message}
    </div>
  );
}

export async function runWithToast(
  action: () => Promise<void>,
  notify: (toast: ToastMessage) => void,
  success: string,
  failure: string,
) {
  try {
    await action();
    notify({ message: success, type: "success" });
    return true;
  } catch (error) {
    const detail = error instanceof Error ? error.message : String(error);
    notify({ message: `${failure}: ${detail}`, type: "error" });
    return false;
  }
}
