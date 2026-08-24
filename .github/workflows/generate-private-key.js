const ethers = require('ethers');
const fs = require('fs');

// Generate a random private key
const privateKey = ethers.Wallet.createRandom().privateKey;

// Create a wallet instance from the private key
const wallet = new ethers.Wallet(privateKey);

// Print the private key and account address
console.log(`Random Ethereum private key: ${privateKey}`);
console.log(`Account address: ${wallet.address}`);
const privateKeyString = `${privateKey}`.substring(2)

if (process.env.GITHUB_OUTPUT) {
  fs.appendFileSync(process.env.GITHUB_OUTPUT, `private_key=${privateKeyString}\n`);
  fs.appendFileSync(process.env.GITHUB_OUTPUT, `account_address=${wallet.address}\n`);
} else {
  // Preserve compatibility for local ad-hoc runs outside GitHub Actions.
  console.log(`::set-output name=private_key::${privateKeyString}`);
  console.log(`::set-output name=account_address::${wallet.address}`);
}
