// Theme utility classes for consistent styling across light/dark modes

export const themeColors = {
  // Background colors
  bg: {
    primary: "bg-white dark:bg-neutral-950",
    secondary: "bg-neutral-50 dark:bg-neutral-900",
    tertiary: "bg-neutral-100 dark:bg-neutral-800",
    card: "bg-white dark:bg-neutral-900",
    cardHover: "hover:bg-neutral-50 dark:hover:bg-neutral-800",
  },

  // Text colors
  text: {
    primary: "text-neutral-900 dark:text-neutral-100",
    secondary: "text-neutral-700 dark:text-neutral-300",
    muted: "text-neutral-500 dark:text-neutral-400",
    inverse: "text-white dark:text-neutral-900",
  },

  // Border colors
  border: {
    primary: "border-neutral-300 dark:border-neutral-700",
    secondary: "border-neutral-200 dark:border-neutral-800",
    light: "border-neutral-100 dark:border-neutral-900",
  },

  // Brand colors (teal)
  brand: {
    bg: "bg-teal-600 dark:bg-teal-500",
    bgHover: "hover:bg-teal-700 dark:hover:bg-teal-600",
    text: "text-teal-600 dark:text-teal-400",
    textHover: "hover:text-teal-700 dark:hover:text-teal-300",
    border: "border-teal-600 dark:border-teal-500",
  },

  // Accent colors (sky)
  accent: {
    bg: "bg-sky-600 dark:bg-sky-500",
    bgHover: "hover:bg-sky-700 dark:hover:bg-sky-600",
    text: "text-sky-600 dark:text-sky-400",
    textHover: "hover:text-sky-700 dark:hover:text-sky-300",
    border: "border-sky-600 dark:border-sky-500",
  },

  // Status colors
  status: {
    success: "text-green-600 dark:text-green-400",
    successBg: "bg-green-50 dark:bg-green-950/30",
    warning: "text-yellow-600 dark:text-yellow-400",
    warningBg: "bg-yellow-50 dark:bg-yellow-950/30",
    error: "text-red-600 dark:text-red-400",
    errorBg: "bg-red-50 dark:bg-red-950/30",
    info: "text-blue-600 dark:text-blue-400",
    infoBg: "bg-blue-50 dark:bg-blue-950/30",
  },

  // Interactive states
  interactive: {
    hover: "hover:bg-neutral-100 dark:hover:bg-neutral-800",
    active: "active:bg-neutral-200 dark:active:bg-neutral-700",
    focus: "focus:ring-2 focus:ring-teal-500 dark:focus:ring-teal-400",
  },
} as const;

// Combined theme classes for common patterns
export const themeClasses = {
  // Page container
  pageContainer: `${themeColors.bg.primary} ${themeColors.text.primary} min-h-screen transition-colors duration-300`,

  // Card
  card: `${themeColors.bg.card} ${themeColors.border.primary} border rounded-lg shadow-sm transition-colors`,
  cardHover: `${themeColors.bg.card} ${themeColors.border.primary} border rounded-lg shadow-sm ${themeColors.interactive.hover} transition-all duration-200`,

  // Input
  input: `${themeColors.bg.primary} ${themeColors.text.primary} ${themeColors.border.primary} border rounded-md px-3 py-2 focus:outline-none ${themeColors.interactive.focus}`,

  // Button primary
  buttonPrimary: `${themeColors.brand.bg} ${themeColors.brand.bgHover} text-white font-medium px-4 py-2 rounded-md transition-colors duration-150 ${themeColors.interactive.focus}`,

  // Button secondary
  buttonSecondary: `${themeColors.bg.secondary} ${themeColors.interactive.hover} ${themeColors.text.primary} font-medium px-4 py-2 rounded-md transition-colors duration-150 ${themeColors.interactive.focus}`,

  // Link
  link: `${themeColors.brand.text} ${themeColors.brand.textHover} underline transition-colors duration-150`,

  // Heading
  heading: `${themeColors.text.primary} font-bold`,
  subheading: `${themeColors.text.secondary} font-semibold`,

  // Divider
  divider: `${themeColors.border.primary} border-t`,
} as const;

// CSS variable based colors (for inline styles)
export const cssVars = {
  background: "var(--color-background)",
  backgroundSecondary: "var(--color-background-secondary)",
  backgroundTertiary: "var(--color-background-tertiary)",
  foreground: "var(--color-foreground)",
  foregroundSecondary: "var(--color-foreground-secondary)",
  foregroundMuted: "var(--color-foreground-muted)",
  border: "var(--color-border)",
  borderSecondary: "var(--color-border-secondary)",
  card: "var(--color-card)",
  cardHover: "var(--color-card-hover)",
  primary: "var(--color-primary)",
  primaryHover: "var(--color-primary-hover)",
  primaryLight: "var(--color-primary-light)",
  accent: "var(--color-accent)",
  accentHover: "var(--color-accent-hover)",
  success: "var(--color-success)",
  warning: "var(--color-warning)",
  error: "var(--color-error)",
  shadowSm: "var(--shadow-sm)",
  shadowMd: "var(--shadow-md)",
  shadowLg: "var(--shadow-lg)",
} as const;
