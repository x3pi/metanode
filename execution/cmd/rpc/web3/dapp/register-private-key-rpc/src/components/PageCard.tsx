import React from "react";
import type { LucideIcon } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import { Badge } from "~/components/ui/badge";
import { Label } from "~/components/ui/label";
import { cn } from "~/lib/utils";

interface PageCardProps {
  title: string;
  description: string;
  icon?: LucideIcon;
  children: React.ReactNode;
  isConnected?: boolean;
  contractAddress?: string;
  colorScheme?: "sky" | "teal" | "purple" | "indigo";
  className?: string;
}

export function PageCard({
  title,
  description,
  icon: Icon,
  children,
  isConnected,
  contractAddress,
  colorScheme = "sky",
  className,
}: PageCardProps) {
  const colorClasses = {
    sky: "border-sky-500/20 text-sky-400",
    teal: "border-teal-500/20 text-teal-400",
    purple: "border-purple-500/20 text-purple-400",
    indigo: "border-indigo-500/20 text-indigo-400",
  };

  return (
    <Card className={cn(colorClasses[colorScheme], className)}>
      <CardHeader className="space-y-3">
        <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
          <CardTitle
            className={cn(
              "text-2xl sm:text-3xl font-bold flex items-center gap-2",
              colorClasses[colorScheme].split(" ")[1]
            )}
          >
            {Icon && <Icon className="h-6 w-6 sm:h-7 sm:w-7" />}
            {title}
          </CardTitle>
          {isConnected !== undefined && (
            <Badge variant={isConnected ? "success" : "outline"}>
              {isConnected ? "Connected" : "Disconnected"}
            </Badge>
          )}
        </div>
        <CardDescription className="text-base">{description}</CardDescription>
        {contractAddress && isConnected && (
          <div className="pt-2">
            <Label className="text-xs text-app-muted">Contract Address</Label>
            <code className="block mt-1 text-xs text-success font-mono bg-app-secondary p-2 rounded">
              {contractAddress}
            </code>
          </div>
        )}
      </CardHeader>

      <CardContent className="space-y-6">{children}</CardContent>
    </Card>
  );
}
