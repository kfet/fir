#!/usr/bin/env bun
// Extract models from models.generated.ts and output as JSON
import * as fs from 'fs';

const input = process.argv[2] || '../pi-mono/packages/ai/src/models.generated.ts';
const content = fs.readFileSync(input, 'utf8');

// Simple regex to extract the MODELS object
// We'll parse it by finding the structure pattern
const lines = content.split('\n');

interface ModelDef {
    id: string;
    name: string;
    api: string;
    provider: string;
    baseUrl: string;
    reasoning: boolean;
    input: string[];
    cost: {
        input: number;
        output: number;
        cacheRead: number;
        cacheWrite: number;
    };
    contextWindow: number;
    maxTokens: number;
    compat?: Record<string, any>;
    headers?: Record<string, string>;
}

const models: Record<string, Record<string, ModelDef>> = {};
let currentProvider: string | null = null;
let currentModelId: string | null = null;
let currentModel: Partial<ModelDef> | null = null;
let inModel = false;

for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    
    // Look for provider key
    const providerMatch = line.match(/^"([^"]+)":\s*{$/);
    if (providerMatch) {
        currentProvider = providerMatch[1];
        models[currentProvider] = {};
        continue;
    }
    
    // Look for model key
    const modelMatch = line.match(/^"([^"]+)":\s*{$/);
    if (modelMatch && currentProvider) {
        currentModelId = modelMatch[1];
        currentModel = {};
        inModel = true;
        continue;
    }
    
    // Parse model fields
    if (inModel && currentModel && currentModelId) {
        const idMatch = line.match(/id:\s*"([^"]+)"/);
        if (idMatch) currentModel.id = idMatch[1];
        
        const nameMatch = line.match(/name:\s*"([^"]+)"/);
        if (nameMatch) currentModel.name = nameMatch[1];
        
        const apiMatch = line.match(/api:\s*"([^"]+)"/);
        if (apiMatch) currentModel.api = apiMatch[1];
        
        const providerMatch = line.match(/provider:\s*"([^"]+)"/);
        if (providerMatch) currentModel.provider = providerMatch[1];
        
        const baseUrlMatch = line.match(/baseUrl:\s*"([^"]+)"/);
        if (baseUrlMatch) currentModel.baseUrl = baseUrlMatch[1];
        
        const reasoningMatch = line.match(/reasoning:\s*(true|false)/);
        if (reasoningMatch) currentModel.reasoning = reasoningMatch[1] === 'true';
        
        const inputMatch = line.match(/input:\s*\[(.*?)\]/);
        if (inputMatch) {
            const inputStr = inputMatch[1];
            currentModel.input = inputStr.split(',').map(s => s.trim().replace(/"/g, ''));
        }
        
        const contextMatch = line.match(/contextWindow:\s*(\d+)/);
        if (contextMatch) currentModel.contextWindow = parseInt(contextMatch[1]);
        
        const maxTokensMatch = line.match(/maxTokens:\s*(\d+)/);
        if (maxTokensMatch) currentModel.maxTokens = parseInt(maxTokensMatch[1]);
        
        // Handle cost object - this is a bit tricky
        const inputCostMatch = line.match(/input:\s*([\d.]+)/);
        const outputCostMatch = line.match(/output:\s*([\d.]+)/);
        const cacheReadMatch = line.match(/cacheRead:\s*([\d.]+)/);
        const cacheWriteMatch = line.match(/cacheWrite:\s*([\d.]+)/);
        
        if (inputCostMatch || outputCostMatch || cacheReadMatch || cacheWriteMatch) {
            if (!currentModel.cost) {
                currentModel.cost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
            }
            if (inputCostMatch && line.includes('input:') && !line.includes('["text')) {
                currentModel.cost.input = parseFloat(inputCostMatch[1]);
            }
            if (outputCostMatch && line.includes('output:')) {
                currentModel.cost.output = parseFloat(outputCostMatch[1]);
            }
            if (cacheReadMatch && line.includes('cacheRead:')) {
                currentModel.cost.cacheRead = parseFloat(cacheReadMatch[1]);
            }
            if (cacheWriteMatch && line.includes('cacheWrite:')) {
                currentModel.cost.cacheWrite = parseFloat(cacheWriteMatch[1]);
            }
        }
        
        // End of model
        if (line.includes('} satisfies') || (line === '},' && inModel)) {
            if (currentModel.id && currentProvider) {
                models[currentProvider][currentModelId!] = currentModel as ModelDef;
            }
            currentModel = null;
            currentModelId = null;
            inModel = false;
        }
    }
    
    // End of provider
    if (line === '},' && !inModel && currentProvider) {
        currentProvider = null;
    }
}

// Output as JSON for further processing
console.log(JSON.stringify(models, null, 2));
