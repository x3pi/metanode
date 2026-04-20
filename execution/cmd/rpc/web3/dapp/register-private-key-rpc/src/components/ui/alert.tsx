import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "~/lib/utils";
import { AlertCircle, CheckCircle2, Info, XCircle } from "lucide-react";

const alertVariants = cva(
  "relative w-full rounded-2xl border-2 p-4 shadow-sm [&>svg~*]:pl-7 [&>svg+div]:translate-y-[-3px] [&>svg]:absolute [&>svg]:left-4 [&>svg]:top-4 transition-all duration-200",
  {
    variants: {
      variant: {
        // Material 3 Surface Variant
        default: "bg-card text-foreground border-border shadow-md",
        // Material 3 Error Container - Text đậm để dễ đọc
        destructive:
          "border-error bg-error-container text-error font-medium [&>svg]:text-error shadow-md",
        // Material 3 Success Container - Text đậm để dễ đọc
        success:
          "border-success bg-success-container text-success font-medium [&>svg]:text-success shadow-md",
        // Material 3 Warning Container - Text đậm, contrast cao để dễ đọc
        warning:
          "border-warning bg-warning-container text-warning font-semibold [&>svg]:text-warning shadow-md dark:text-warning dark:font-bold",
        // Material 3 Info Container - Text đậm để dễ đọc
        info: "border-info bg-info-container text-info font-medium [&>svg]:text-info shadow-md",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  }
);

const Alert = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement> &
    VariantProps<typeof alertVariants> & { icon?: boolean }
>(({ className, variant, icon = true, children, ...props }, ref) => {
  const Icon =
    variant === "destructive"
      ? XCircle
      : variant === "success"
      ? CheckCircle2
      : variant === "warning"
      ? AlertCircle
      : variant === "info"
      ? Info
      : AlertCircle;

  return (
    <div
      ref={ref}
      role="alert"
      className={cn(
        alertVariants({ variant }),
        "overflow-wrap-anywhere",
        className
      )}
      {...props}
    >
      {icon && <Icon className="h-4 w-4" />}
      {children}
    </div>
  );
});
Alert.displayName = "Alert";

const AlertTitle = React.forwardRef<
  HTMLParagraphElement,
  React.HTMLAttributes<HTMLHeadingElement>
>(({ className, ...props }, ref) => (
  <h5
    ref={ref}
    className={cn("mb-1 font-medium leading-none tracking-tight", className)}
    {...props}
  />
));
AlertTitle.displayName = "AlertTitle";

const AlertDescription = React.forwardRef<
  HTMLParagraphElement,
  React.HTMLAttributes<HTMLParagraphElement>
>(({ className, ...props }, ref) => (
  <div
    ref={ref}
    className={cn("text-sm font-medium [&_p]:leading-relaxed", className)}
    {...props}
  />
));
AlertDescription.displayName = "AlertDescription";

export { Alert, AlertTitle, AlertDescription };
