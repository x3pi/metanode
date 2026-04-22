import { Moon, Sun } from "lucide-react";
import { useTheme } from "~/contexts/ThemeContext";
import { Button } from "~/components/ui/button";

export function ThemeToggle() {
  const { theme, toggleTheme } = useTheme();

  return (
    <Button
      variant="outline"
      size="sm"
      onClick={toggleTheme}
      className="h-9 px-3 gap-2 border-(--color-border) hover:bg-(--color-background-secondary)"
      aria-label="Toggle theme"
    >
      {theme === "light" ? (
        <>
          <Moon className="h-4 w-4" />
          <span className="text-xs font-medium hidden sm:inline">Dark</span>
        </>
      ) : (
        <>
          <Sun className="h-4 w-4" />
          <span className="text-xs font-medium hidden sm:inline">Light</span>
        </>
      )}
    </Button>
  );
}
