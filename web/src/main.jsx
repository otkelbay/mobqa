import { createRoot } from 'react-dom/client';
import './styles/global.css';
import { App } from './App.jsx';

const container = document.getElementById('root');
const root = container.__qaAgentRoot || createRoot(container);
container.__qaAgentRoot = root;

root.render(<App />);
