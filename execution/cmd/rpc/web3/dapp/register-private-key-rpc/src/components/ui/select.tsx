import * as React from "react";
import { ChevronDown } from "lucide-react";
import { cn } from "~/lib/utils";

export interface SelectProps
  extends React.SelectHTMLAttributes<HTMLSelectElement> {
  options: { value: string; label: string }[];
}

const Select = React.forwardRef<HTMLSelectElement, SelectProps>(
  ({ className, options, ...props }, ref) => {
    return (
      <div className="relative">
        <select
          className={cn(
            "w-full h-10 rounded-lg border border-neutral-300 dark:border-neutral-600 bg-white dark:bg-neutral-700/60 px-3 py-2 text-sm text-neutral-900 dark:text-neutral-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-500 focus-visible:ring-offset-2 focus-visible:ring-offset-white dark:focus-visible:ring-offset-neutral-800 disabled:cursor-not-allowed disabled:opacity-50 appearance-none cursor-pointer transition-colors duration-200",
            className
          )}
          ref={ref}
          {...props}
        >
          {options.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
        <ChevronDown className="absolute right-3 top-1/2 -translate-y-1/2 h-4 w-4 text-neutral-400 dark:text-neutral-500 pointer-events-none" />
      </div>
    );
  }
);
Select.displayName = "Select";

export { Select };
