/**
 * Hệ thống màu sắc tập trung - Chỉ cần đổi màu ở file index.css
 * File này chỉ là mapping để dễ dùng trong TypeScript
 *
 * CÁCH DÙNG:
 *
 * 1. Dùng CSS classes (Khuyên dùng):
 *    <div className="bg-app text-app">Content</div>
 *    <button className="bg-primary text-white">Button</button>
 *
 * 2. Dùng trong inline styles:
 *    <div style={{ color: AppColors.primary }}>Content</div>
 *
 * 3. Dùng với cn() utility:
 *    className={cn("bg-primary", someCondition && "bg-error")}
 */

// CSS Classes - Dùng trực tiếp trong className
export const AppColorClasses = {
  // Background
  bg: {
    primary: "bg-app",
    secondary: "bg-app-secondary",
    card: "bg-white dark:bg-neutral-900",
  },

  // Text
  text: {
    primary: "text-app",
    secondary: "text-app-secondary",
    muted: "text-app-muted",
  },

  // Brand (teal)
  brand: {
    bg: "bg-primary",
    bgHover: "bg-primary-hover",
    text: "text-primary",
  },

  // Status
  status: {
    success: "text-success",
    successBg: "bg-success",
    error: "text-error",
    errorBg: "bg-error",
  },
} as const;

// CSS Variables - Dùng trong inline styles
export const AppColors = {
  primary: "var(--color-primary)",
  primaryHover: "var(--color-primary-hover)",

  background: "var(--color-background)",
  backgroundSecondary: "var(--color-background-secondary)",

  foreground: "var(--color-foreground)",
  foregroundSecondary: "var(--color-foreground-secondary)",
  foregroundMuted: "var(--color-foreground-muted)",

  border: "var(--color-border)",

  success: "var(--color-success)",
  error: "var(--color-error)",
  warning: "var(--color-warning)",
  info: "var(--color-info)",
} as const;

/**
 * VÍ DỤ SỬ DỤNG:
 *
 * // Trong component:
 * import { AppColorClasses } from '~/styles/colors';
 *
 * function MyComponent() {
 *   return (
 *     <div className={AppColorClasses.bg.primary}>
 *       <h1 className={AppColorClasses.brand.text}>Title</h1>
 *       <p className={AppColorClasses.text.secondary}>Description</p>
 *     </div>
 *   );
 * }
 *
 * // Khi muốn đổi màu brand từ teal -> blue:
 * // Chỉ cần sửa trong src/index.css:
 * // --color-primary: #3b82f6; (blue-600)
 * // Tất cả nơi dùng sẽ tự động đổi màu!
 */
