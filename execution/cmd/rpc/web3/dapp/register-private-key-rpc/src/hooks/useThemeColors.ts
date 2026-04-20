import { useTheme } from "~/contexts/ThemeContext";

/**
 * Hook to get theme-aware color values
 * Returns CSS variable values that automatically update with theme
 */
export function useThemeColors() {
  const { theme } = useTheme();

  return {
    isDark: theme === "dark",
    isLight: theme === "light",

    // Get CSS variable value
    getCSSVar: (varName: string) => {
      if (typeof window === "undefined") return "";
      return getComputedStyle(document.documentElement)
        .getPropertyValue(varName)
        .trim();
    },

    // Conditional class helper
    when: (condition: boolean, classes: string) => (condition ? classes : ""),

    // Theme-specific classes
    ifDark: (classes: string) => (theme === "dark" ? classes : ""),
    ifLight: (classes: string) => (theme === "light" ? classes : ""),
  };
}

/**
 * Get color value from CSS variable
 */
export function getCSSVariable(varName: string): string {
  if (typeof window === "undefined") return "";
  return getComputedStyle(document.documentElement)
    .getPropertyValue(varName)
    .trim();
}
