import test from 'node:test';
import assert from 'node:assert/strict';
import {JSDOM} from 'jsdom';
import {publicEnv,readRuntimeConfiguration} from '../dist/browser.js';

test('marked custom bootstrap exposes eager and lazy configuration before bootstrap runs',async()=>{
 const dom=new JSDOM('<script type="application/json" id="custom" data-gobeyond-bootstrap>{"deploymentRevision":"revision","publicConfig":{"KEY":"pk_test"}}</script>');
 try {
  assert.equal(publicEnv('KEY',dom.window.document),'pk_test');
  const lazy=await import('../dist/runtime-config.js');
  assert.equal(lazy.publicEnv('KEY',dom.window.document),'pk_test');
  assert.equal(readRuntimeConfiguration(dom.window.document).deploymentRevision,'revision');
  assert.equal(publicEnv('__proto__',dom.window.document),undefined);
 }finally {dom.window.close()}
});
