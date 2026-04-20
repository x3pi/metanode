import { forwardRef } from "react";
import { Button, type ButtonProps } from "./button";
import { cn } from "~/lib/utils";

/**
 * AppButton - Button component với styles tối ưu cho cả Light/Dark mode
 * Sử dụng CSS variables từ index.css để dễ dàng tùy chỉnh màu sắc
 */

export interface AppButtonProps extends ButtonProps {
  /**
   * Variant của button
   * - primary: Button chính (xanh dương)
   * - success: Button thành công (xanh lá)
   * - danger: Button nguy hiểm (đỏ)
   * - outline: Button viền
   * - ghost: Button trong suốt
   */
  appVariant?: "primary" | "success" | "danger" | "outline" | "ghost";
}

const AppButton = forwardRef<HTMLButtonElement, AppButtonProps>(
  ({ className, appVariant = "outline", disabled, ...props }, ref) => {
    // Base styles cho tất cả buttons
    const baseStyles = "transition-all duration-200 font-medium";

    // Variant styles - Contrast cao, text luôn rõ khi hover
    const variantStyles = {
      primary: cn(
        "bg-primary hover:bg-primary-hover text-white hover:text-white font-bold",
        "disabled:opacity-50 disabled:cursor-not-allowed",
        "shadow-sm hover:shadow-md dark:bg-primary dark:text-white dark:hover:text-white"
      ),
      success: cn(
        "bg-success text-white hover:text-white hover:bg-success font-bold",
        "disabled:opacity-50 disabled:cursor-not-allowed",
        "shadow-sm hover:shadow-md dark:bg-success dark:text-white dark:hover:text-white"
      ),
      danger: cn(
        "bg-error text-white hover:text-white hover:bg-error font-bold",
        "disabled:opacity-50 disabled:cursor-not-allowed",
        "shadow-sm hover:shadow-md dark:bg-error dark:text-white dark:hover:text-white"
      ),
      outline: cn(
        "border-2 border-primary bg-transparent text-primary font-bold",
        "hover:bg-primary hover:!text-white dark:hover:bg-primary dark:hover:!text-white",
        "transition-all duration-200 relative z-10",
        "disabled:opacity-50 disabled:cursor-not-allowed"
      ),
      ghost: cn(
        "bg-transparent text-foreground hover:bg-card-hover hover:text-foreground font-bold",
        "dark:text-foreground dark:hover:bg-card-hover dark:hover:text-foreground",
        "disabled:opacity-50 disabled:cursor-not-allowed"
      ),
    };

    return (
      <Button
        ref={ref}
        className={cn(baseStyles, variantStyles[appVariant], className)}
        disabled={disabled}
        {...props}
      />
    );
  }
);

AppButton.displayName = "AppButton";

export { AppButton };
