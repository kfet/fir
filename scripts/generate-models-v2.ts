#!/usr/bin/env bun
import * as fs from 'fs';
import * as path from 'path';

const inputFile = process.argv[2] || '../pi-mono/packages/ai/src/models.generated.ts';

if (!fs.existsSync(inputFile)) {
    console.error(`Error: ${inputFile} not found`);
    process.exit(1);
}

const content = fs.readFileSync(inputFile, 'utf8');

// Remove TypeScript specific syntax to make it valid JS
let jsContent = content
    .replace(/import type.*$/gm, '')
    .replace(/export const MODELS = /, 'const MODELS = ')
    .replace(/} satisfies Model<[^>]+>/g, '}')
    .replace(/} as const;/, '};');

// Parse the JavaScript
let MODELS: Record<string, Record<string, any>> = {};
try {
    // Create a context to evaluate
    const ctx = { MODELS: {} };
    // Use Function constructor instead of eval for better control
    const evalFunc = new Function('const MODELS = ' + jsContent.split('const MODELS = ')[1]);
    // This won't work directly, let's use a different approach
    
    // Actually, let's just regex extract the providers and models
    const providerMatches = jsContent.matchAll(/"([^"]+)":\s*{(?:[^{}]|{[^}]*})*?{(?:[^{}]|{[^}]*})*?}/g);
    
    // Better approach: split by provider blocks
    const modelsMatch = jsContent.match(/const MODELS = ({[\s\S]*}) as const;/);
    if (!modelsMatch) {
        console.error('Could not find MODELS declaration');
        process.exit(1);
    }
    
    // Extract provider sections
    const modelsText = modelsMatch[1];
    
    // Use a regex to extract each provider block
    let currentPos = 0;
    const providerBlocks: Record<string, string> = {};
    let lastProvider = '';
    let braceDepth = 0;
    let providerStart = -1;
    
    // Simple state machine to extract providers
    for (let i = 0; i < modelsText.length; i++) {
        const char = modelsText[i];
        const prevChar = i > 0 ? modelsText[i - 1] : '';
        
        // Detect provider key
        if (char === '"' && modelsText[i + 1] && !lastProvider) {
            const endQuote = modelsText.indexOf('"', i + 1);
            if (endQuote > 0 && modelsText[endQuote + 1] === ':') {
                lastProvider = modelsText.substring(i + 1, endQuote);
                providerStart = i;
                i = endQuote + 1;
            }
        }
        
        // Track braces to find the end of this provider's object
        if (lastProvider && char === '{') braceDepth++;
        if (lastProvider && char === '}') {
            braceDepth--;
            if (braceDepth === 0) {
                providerBlocks[lastProvider] = modelsText.substring(providerStart, i + 1);
                lastProvider = '';
            }
        }
    }
    
    // Parse each provider block to extract models
    for (const [provider, blockText] of Object.entries(providerBlocks)) {
        MODELS[provider] = {};
        
        // Extract models from this provider's block
        const modelMatches = blockText.matchAll(/"([^"]+)":\s*{([^}]|\n)*?contextWindow:[^,]*,\s*maxTokens:[^}]*}/g);
        // This is getting too complex. Let's use a simpler JSON approach
    }
} catch (e) {
    console.error('Error parsing MODELS:', e);
    process.exit(1);
}

console.log('// Generated models file');
console.log(JSON.stringify(MODELS, null, 2));
