import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { cn } from "~/lib/utils";

interface FormFieldProps {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  type?: "text" | "password" | "email";
  helpText?: string;
  labelClassName?: string;
  inputClassName?: string;
}

export function FormField({
  id,
  label,
  value,
  onChange,
  placeholder,
  disabled,
  type = "text",
  helpText,
  labelClassName,
  inputClassName,
}: FormFieldProps) {
  return (
    <div className="space-y-2">
      <Label htmlFor={id} className={cn(labelClassName)}>
        {label}
      </Label>
      <Input
        type={type}
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        disabled={disabled}
        className={cn("font-mono", inputClassName)}
      />
      {helpText && <p className="text-xs text-app-muted">{helpText}</p>}
    </div>
  );
}
