// src/components/Sidebar.tsx
import React, { useState } from 'react';
import { NavLink } from 'react-router-dom';
import { useMobileMenu } from '../contexts/MobileMenuContext';
import type { PageLink } from '../App';

interface SidebarProps {
  pageLinks: PageLink[];
}

export const Sidebar: React.FC<SidebarProps> = ({ pageLinks }) => {
  const [isCollapsed, setIsCollapsed] = useState(false);
  const { isMobileMenuOpen, closeMobileMenu } = useMobileMenu();
  // Desktop sidebar - Material 3 Navigation Drawer
  const DesktopSidebar = () => (
    <aside
      className={`hidden md:flex h-[calc(100vh-4rem)] bg-card border-r border-border shadow-md transition-all duration-300 flex-col flex-shrink-0 dark:bg-card dark:border-border ${
        isCollapsed ? 'w-16' : 'w-64'
      }`}
    >
      <SidebarContent 
        isCollapsed={isCollapsed} 
        pageLinks={pageLinks} 
        showToggleButton={true} 
        onToggle={() => setIsCollapsed(!isCollapsed)} 
      />
    </aside>
  );

  // Mobile sidebar overlay  
  const MobileSidebar = () => (
    <>
      {isMobileMenuOpen && (
        <div
          className="md:hidden fixed inset-0 bg-app/80 backdrop-blur-sm z-40"
          onClick={() => closeMobileMenu()}
        />
      )}

      <aside
        id="mobile-sidebar"
        className={`md:hidden fixed left-0 top-16 h-[calc(100vh-4rem)] w-64 bg-card border-r border-border transition-all duration-300 ease-in-out shadow-xl z-50 transform ${
          isMobileMenuOpen ? 'translate-x-0' : '-translate-x-full'
        }`}
      >
        <SidebarContent 
          isCollapsed={false} 
          pageLinks={pageLinks} 
          onLinkClick={() => closeMobileMenu()} 
          showCloseButton={false}
        />
      </aside>
    </>
  );

  return (
    <>
      <DesktopSidebar />
      <MobileSidebar />
    </>
  );
};

// Sidebar content component
const SidebarContent: React.FC<{
  isCollapsed: boolean;
  pageLinks: PageLink[];
  onLinkClick?: () => void;
  showToggleButton?: boolean;
  onToggle?: () => void;
  showCloseButton?: boolean;
  onClose?: () => void;
}> = ({ isCollapsed, pageLinks, onLinkClick, showToggleButton, onToggle, showCloseButton, onClose }) => (
  <div className="flex flex-col h-full">
    {/* Header */}
    {(showToggleButton || showCloseButton) && (
      <div className="p-4 border-b border-border flex items-center justify-between">
        {showCloseButton ? (
          <>
            <h2 className="text-lg font-bold text-primary">Menu</h2>
            <button
              onClick={onClose}
              className="w-8 h-8 flex items-center justify-center text-primary hover:text-primary-hover hover:bg-primary/10 rounded-full transition-all duration-200 active:scale-[0.95]"
              aria-label="Close menu"
            >
              ✕
            </button>
          </>
        ) : showToggleButton && !isCollapsed ? (
          <>
            <h2 className="text-sm font-bold text-primary">Navigation</h2>
            <button
              onClick={onToggle}
              className="w-8 h-8 bg-primary hover:bg-primary-hover rounded-full flex items-center justify-center text-white shadow-md hover:shadow-lg transition-all duration-200 active:scale-[0.95] font-bold dark:bg-primary dark:text-white"
              title="Collapse sidebar"
            >
              <span className="text-xs font-bold">←</span>
            </button>
          </>
        ) : null}
        
        {/* Toggle for collapsed */}
        {showToggleButton && isCollapsed && (
          <div className="w-full flex justify-center">
            <button
              onClick={onToggle}
              className="w-8 h-8 bg-primary hover:bg-primary-hover rounded-full flex items-center justify-center text-white shadow-md hover:shadow-lg transition-all duration-200 active:scale-[0.95] font-bold dark:bg-primary dark:text-white"
              title="Expand sidebar"
            >
              <span className="text-xs font-bold">→</span>
            </button>
          </div>
        )}
      </div>
    )}

    {/* Mobile header without close button */}
    {!showToggleButton && !showCloseButton && (
      <div className="p-4 border-b border-border">
        <h2 className="text-lg font-bold text-primary">Menu</h2>
        <p className="text-xs text-app-muted mt-1">Tap outside to close</p>
      </div>
    )}

    {/* Navigation */}
    <nav className="flex-1 px-3 py-6 space-y-1 overflow-y-auto">
      {!isCollapsed && !showCloseButton && (
        <div className="px-3 mb-4">
          <h2 className="text-xs font-bold uppercase tracking-wider text-app-muted">
            Navigation
          </h2>
        </div>
      )}

      {pageLinks.map((link) => (
        <NavLink
          key={link.path}
          to={link.path}
          onClick={onLinkClick}
          title={isCollapsed ? link.label : undefined}
          className={({ isActive }) => {
            const baseClass = `flex items-center px-4 py-3 rounded-2xl text-sm font-medium transition-all duration-200 ease-in-out ${
              isCollapsed ? 'justify-center' : ''
            }`;
            if (isActive) {
              return `${baseClass} bg-primary/20 text-primary shadow-sm border-l-4 border-primary font-bold dark:bg-primary/30 dark:text-primary`;
            }
            return `${baseClass} text-foreground hover:bg-primary/15 hover:text-primary font-semibold dark:text-foreground dark:hover:bg-primary/25`;
          }}
        >
          {({ isActive }) => (
            <>
              {!isCollapsed && (
                <>
                  {isActive && (
                    <div className="w-1 h-1 rounded-full mr-3 bg-primary" />
                  )}
                  {link.label}
                </>
              )}
              {isCollapsed && (
                <span className="text-lg">
                  {link.path === '/' && '🏠'}
                  {link.path === '/bls' && '🔐'}
                  {link.path === '/account-type' && '⚙️'}
                  {link.path === '/register-rpc' && '📝'}
                  {link.path === '/accounts' && '📋'}
                </span>
              )}
            </>
          )}
        </NavLink>
      ))}
    </nav>
  </div>
);
