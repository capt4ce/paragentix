import React, { useState } from "react";
import { LoaderCircle } from "lucide-react";

type AsyncButtonProps = Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "onClick"> & {
  onClick: (event: React.MouseEvent<HTMLButtonElement>) => void | Promise<void>;
};

export function AsyncButton({
  children,
  disabled,
  onClick,
  ...props
}: AsyncButtonProps) {
  const [loading, setLoading] = useState(false);
  const accessibleLabel = props["aria-label"] ??
    (typeof children === "string" ? children : undefined);

  return (
    <button
      {...props}
      aria-label={loading ? accessibleLabel : props["aria-label"]}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      onClick={async (event) => {
        if (loading) return;
        setLoading(true);
        try {
          await onClick(event);
        } finally {
          setLoading(false);
        }
      }}
    >
      {loading ? <LoaderCircle className="size-4 animate-spin" aria-hidden="true" /> : children}
    </button>
  );
}
