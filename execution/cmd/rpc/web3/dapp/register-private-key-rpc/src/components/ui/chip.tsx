import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "~/lib/utils";

const chipVariants = cva(
  "inline-flex items-center justify-center rounded-full px-3 py-1 text-xs font-medium transition-all duration-200 focus:outline-none focus:ring-2 focus:ring-offset-2",
  {
    variants: {
      variant: {
        // Material 3 Filled Chip - Text trắng đậm
        default:
          "bg-primary text-white shadow-sm hover:shadow-md font-bold dark:bg-primary dark:text-white",
        // Material 3 Tonal Chip - Background đậm, text trắng
        secondary:
          "bg-secondary text-white shadow-sm hover:bg-secondary-hover font-bold dark:bg-gray-600 dark:text-white",
        // Material 3 Outlined Chip - Text đậm
        outlined:
          "border-2 border-primary bg-transparent text-primary hover:bg-primary hover:text-white font-bold dark:border-primary dark:text-primary dark:hover:bg-primary dark:hover:text-white",
        // Material 3 Error Chip - Text trắng đậm
        error:
          "bg-error text-white shadow-sm hover:shadow-md font-bold dark:bg-error dark:text-white",
        // Material 3 Success Chip - Text trắng đậm
        success:
          "bg-success text-white shadow-sm hover:shadow-md font-bold dark:bg-success dark:text-white",
        // Material 3 Warning Chip - Text trắng đậm
        warning:
          "bg-warning text-white shadow-sm hover:shadow-md font-bold dark:bg-warning dark:text-white",
      },
      size: {
        default: "px-3 py-1 text-xs",
        sm: "px-2 py-0.5 text-[10px]",
        lg: "px-4 py-1.5 text-sm",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
);

export interface ChipProps
  extends React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof chipVariants> {}

const Chip = React.forwardRef<HTMLDivElement, ChipProps>(
  ({ className, variant, size, ...props }, ref) => {
    return (
      <div
        ref={ref}
        className={cn(chipVariants({ variant, size }), className)}
        {...props}
      />
    );
  }
);
Chip.displayName = "Chip";

export { Chip, chipVariants };

