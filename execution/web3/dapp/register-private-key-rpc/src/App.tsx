import { useState } from 'react';
import './App.css'; //
import MetaMaskSignerPage from './MetaMaskSigner'; //
import BlsTransactionPage from './BlsManagerPage'; //
import SetAccountType from './SetAccountType';
import HomePage from './HomePage';

import SharedHeader from './components/SharedHeader';

export type PageName = 'Home' | 'SetAccountType' | 'bls' | 'metamask'; //

export interface PageLink {
  name: PageName;
  label: string;
}

function App() {
  const [currentPage, setCurrentPage] = useState<PageName>('Home');

  const pageLinks: PageLink[] = [
    
    { name: 'bls', label: 'Publickey BLS' }, //
    { name: 'SetAccountType', label: 'AccountType' },
    { name: 'metamask', label: 'Register BLS Rpc' }, //
  ];

  const renderPage = () => { //
    switch (currentPage) {
      case 'bls':
        return <BlsTransactionPage />;
      case 'SetAccountType':
        return <SetAccountType />;
      case 'metamask':
        return <MetaMaskSignerPage />;
      case 'Home':
          return <HomePage />;
      default:
        return <HomePage />;
    }
  };

  return (
    <div className="bg-neutral-950 min-h-screen text-neutral-100 flex flex-col">
      <SharedHeader
        currentPage={currentPage}
        setCurrentPage={setCurrentPage}
        pageLinks={pageLinks}
      />

      <main className="flex-grow container mx-auto px-2 sm:px-4"> {/* */}
        {renderPage()}
      </main>

      <footer className="text-center py-6 text-xs text-neutral-600 bg-neutral-900 mt-auto"> {/* */}
      <p>An account management application.</p>
      </footer>
    </div>
  );
}

export default App;