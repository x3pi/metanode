import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "~/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap text-sm font-bold transition-all duration-200 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50 disabled:cursor-not-allowed",
  {
      variants: {
        variant: {
          // Material 3 Filled Button - Text trắng đậm, luôn rõ khi hover
          default:
            "bg-primary text-white hover:text-white shadow-md hover:shadow-lg hover:bg-primary-hover active:scale-[0.98] focus-visible:ring-primary rounded-full font-bold dark:bg-primary dark:text-white dark:hover:text-white",
          // Material 3 Filled Tonal Button - Text trắng, luôn rõ khi hover
          secondary:
            "bg-gray-800 hover:bg-gray-900 text-white hover:text-white shadow-md hover:shadow-lg active:scale-[0.98] focus-visible:ring-border rounded-full font-bold dark:bg-gray-600 dark:text-white dark:hover:bg-gray-500 dark:hover:text-white",
          // Material 3 Outlined Button - Text rõ khi hover, không bị che
          outline:
            "border-2 border-primary bg-transparent text-primary hover:bg-primary  active:scale-[0.98] focus-visible:ring-primary rounded-full font-bold transition-all duration-200 relative z-10 dark:border-primary dark:text-primary dark:hover:bg-primary dark:hover:!text-white",
          // Material 3 Text Button - Text rõ khi hover
          ghost: 
            "bg-transparent text-primary hover:bg-primary/20 hover:text-primary active:scale-[0.98] focus-visible:ring-primary rounded-full font-bold dark:text-primary dark:hover:bg-primary/30 dark:hover:text-primary",
          // Material 3 Destructive Button - Text trắng, luôn rõ khi hover
          destructive:
            "bg-error text-white hover:text-white shadow-md hover:shadow-lg hover:bg-error active:scale-[0.98] focus-visible:ring-error rounded-full font-bold dark:bg-error dark:text-white dark:hover:text-white dark:hover:bg-error",
          // Material 3 Link Button - Text đậm
          link: 
            "bg-transparent text-primary underline-offset-4 hover:underline focus-visible:ring-primary p-0 h-auto font-bold",
        },
      size: {
        default: "h-10 px-6 py-2.5",
        sm: "h-8 px-4 py-2 text-xs rounded-full",
        lg: "h-12 px-8 py-3 text-base rounded-full",
        icon: "h-10 w-10 rounded-full",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  }
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, ...props }, ref) => {
    return (
      <button
        className={cn(buttonVariants({ variant, size, className }))}
        ref={ref}
        {...props}
      />
    );
  }
);
Button.displayName = "Button";

export { Button, buttonVariants };
