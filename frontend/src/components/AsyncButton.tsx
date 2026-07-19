import React, { useState } from "react";

type AsyncButtonProps = Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "onClick"> & {
  onClick: (event: React.MouseEvent<HTMLButtonElement>) => void | Promise<void>;
  loadingLabel?: string;
};

export function AsyncButton({
  children,
  disabled,
  loadingLabel = "Loading…",
  onClick,
  ...props
}: AsyncButtonProps) {
  const [loading, setLoading] = useState(false);

  return (
    <button
      {...props}
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
      {loading ? loadingLabel : children}
    </button>
  );
}
