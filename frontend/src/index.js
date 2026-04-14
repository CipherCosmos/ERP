import React from 'react';
import ReactDOM from 'react-dom/client';
import './i18n';
import App from './App';
import * as serviceWorkerRegistration from './serviceWorkerRegistration';

const root = ReactDOM.createRoot(document.getElementById('root'));
root.render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);

// Register the service worker for offline-first capabilities.
// In production this is enabled by default.
// To also enable in development, pass { enableInDev: true }.
serviceWorkerRegistration.register({
  onSuccess: (registration) => {
    console.log('GBOS service worker registered successfully', registration);
  },
  onUpdate: (registration) => {
    console.log('GBOS service worker updated', registration);
  },
  onOffline: () => {
    console.log('GBOS app is running in offline mode');
  },
});