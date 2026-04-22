import type { Config } from "tailwindcss";

export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  darkMode: "class", // Bật dark mode với class .dark
  theme: {
    extend: {
      // Extend với CSS variables
      colors: {
        border: "var(--color-border)",
        card: "var(--color-card)",
        "card-hover": "var(--color-card-hover)",
        foreground: "var(--color-foreground)",
        "foreground-secondary": "var(--color-foreground-secondary)",
        primary: {
          DEFAULT: "var(--color-primary)",
          hover: "var(--color-primary-hover)",
        },
        success: "var(--color-success)",
        error: "var(--color-error)",
        warning: "var(--color-warning)",
        purple: {
          DEFAULT: "var(--color-purple)",
          hover: "var(--color-purple-hover)",
        },
        teal: {
          DEFAULT: "var(--color-teal)",
          hover: "var(--color-teal-hover)",
        },
      },
    },
  },
  plugins: [],
} satisfies Config;
