import { Alert, AlertDescription, AlertTitle } from "~/components/ui/alert";

interface StatusDisplayProps {
  status?: string;
  error?: string;
}

export function StatusDisplay({ status, error }: StatusDisplayProps) {
  if (!status && !error) return null;

  if (error) {
    return (
      <Alert variant="destructive">
        <AlertTitle>Error</AlertTitle>
        <AlertDescription className="text-xs wrap-break-word">
          {error}
        </AlertDescription>
      </Alert>
    );
  }

  if (status) {
    return (
      <Alert variant={status.includes("successful") ? "success" : "info"}>
        <AlertTitle>
          {status.includes("successful") ? "Success" : "Status"}
        </AlertTitle>
        <AlertDescription className="text-xs whitespace-pre-wrap wrap-break-word">
          {status}
        </AlertDescription>
      </Alert>
    );
  }

  return null;
}
