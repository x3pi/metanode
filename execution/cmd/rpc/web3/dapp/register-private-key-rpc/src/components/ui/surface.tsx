import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "~/lib/utils";

const surfaceVariants = cva(
  "rounded-3xl transition-all duration-300",
  {
    variants: {
      variant: {
        // Material 3 Surface
        default: "bg-card border border-border shadow-md",
        // Material 3 Surface Variant
        variant: "bg-app-secondary border border-border shadow-sm",
        // Material 3 Surface Container
        container: "bg-app-tertiary border border-border shadow-sm",
        // Material 3 Elevated Surface
        elevated: "bg-card border border-border shadow-lg hover:shadow-xl",
      },
      elevation: {
        none: "shadow-none",
        sm: "shadow-sm",
        md: "shadow-md",
        lg: "shadow-lg",
        xl: "shadow-xl",
      },
    },
    defaultVariants: {
      variant: "default",
      elevation: "md",
    },
  }
);

export interface SurfaceProps
  extends React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof surfaceVariants> {}

const Surface = React.forwardRef<HTMLDivElement, SurfaceProps>(
  ({ className, variant, elevation, ...props }, ref) => {
    return (
      <div
        ref={ref}
        className={cn(surfaceVariants({ variant, elevation }), className)}
        {...props}
      />
    );
  }
);
Surface.displayName = "Surface";

export { Surface, surfaceVariants };

