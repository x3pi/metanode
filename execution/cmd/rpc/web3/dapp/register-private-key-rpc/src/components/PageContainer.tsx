import React from "react";
import { cn } from "~/lib/utils";

interface PageContainerProps {
  children: React.ReactNode;
  className?: string;
  maxWidth?: "sm" | "md" | "lg" | "xl" | "2xl" | "4xl" | "full";
}

export function PageContainer({
  children,
  className,
  maxWidth = "full",
}: PageContainerProps) {
  const maxWidthClasses = {
    sm: "max-w-sm",
    md: "max-w-md",
    lg: "max-w-lg",
    xl: "max-w-xl",
    "2xl": "max-w-2xl",
    "4xl": "max-w-4xl",
    full: "max-w-full", // Full width option
  };

  return (
    <div className="w-full transition-colors duration-300">
      <div
        className={cn(
          "w-full space-y-4 sm:space-y-6",
          maxWidth !== "full" && maxWidthClasses[maxWidth] && "mx-auto",
          className
        )}
      >
        {children}
      </div>
    </div>
  );
}
