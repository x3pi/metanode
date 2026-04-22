import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "~/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center rounded-full border px-3 py-1 text-xs font-medium transition-all duration-200 focus:outline-none focus:ring-2 focus:ring-offset-2",
  {
    variants: {
      variant: {
        // Material 3 Filled Badge - Text trắng đậm
        default: "border-transparent bg-primary text-white shadow-sm hover:shadow-md font-bold dark:bg-primary dark:text-white",
        // Material 3 Tonal Badge - Background đậm, text trắng
        secondary:
          "border-transparent bg-slate-600 text-white shadow-sm hover:bg-slate-700 font-bold dark:bg-gray-600 dark:text-white",
        // Material 3 Error Badge - Text trắng đậm
        destructive:
          "border-transparent bg-error text-white shadow-sm hover:shadow-md font-bold dark:bg-error dark:text-white",
        // Material 3 Success Badge - Text trắng đậm
        success:
          "border-transparent bg-success text-white shadow-sm hover:shadow-md font-bold dark:bg-success dark:text-white",
        // Material 3 Warning Badge - Text trắng đậm
        warning:
          "border-transparent bg-warning text-white shadow-sm hover:shadow-md font-bold dark:bg-warning dark:text-white",
        // Material 3 Outlined Badge - Text đậm
        outline: "text-foreground border-2 border-primary bg-transparent hover:bg-primary hover:text-white font-bold dark:border-primary dark:text-primary dark:hover:bg-primary dark:hover:text-white",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return (
    <div className={cn(badgeVariants({ variant }), className)} {...props} />
  );
}

export { Badge, badgeVariants };
