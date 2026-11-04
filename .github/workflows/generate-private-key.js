const ethers = require('ethers');
const fs = require('fs');

// Generate a random private key
const privateKey = ethers.Wallet.createRandom().privateKey;

// Create a wallet instance from the private key
const wallet = new ethers.Wallet(privateKey);

const privateKeyString = `${privateKey}`.substring(2)

// Mask both forms before publishing the key. The output and the later steps use the
// bare hex, so masking only the 0x form would not cover them.
console.log(`::add-mask::${privateKey}`);
console.log(`::add-mask::${privateKeyString}`);
console.log(`Account address: ${wallet.address}`);

if (process.env.GITHUB_OUTPUT) {
  fs.appendFileSync(process.env.GITHUB_OUTPUT, `private_key=${privateKeyString}\n`);
  fs.appendFileSync(process.env.GITHUB_OUTPUT, `account_address=${wallet.address}\n`);
} else {
  // Preserve compatibility for local ad-hoc runs outside GitHub Actions.
  console.log(`::set-output name=private_key::${privateKeyString}`);
  console.log(`::set-output name=account_address::${wallet.address}`);
}
