async function tokenPool(api, token) {
    const tokenPoolMap = new Map();
    const tokenPoolEntries = await api.query.vtokenMinting.tokenPool.entries();
  
    tokenPoolEntries.forEach((tokenPool) => {
      let key = tokenPool[0].toHuman();
      let value = tokenPool[1].toHuman();
      if (key[0].Token) {
        tokenPoolMap.set(key[0].Token, value);
      }
    });
  
    return tokenPoolMap.get(token);
  }

async function tokenIssuance(api, token) {
    const tokenIssuanceMap = new Map();
    const totalIssuance = await api.query.tokens.totalIssuance.entries();
  
    totalIssuance.forEach((totalIssuance) => {
      let key = totalIssuance[0].toHuman();
      let value = totalIssuance[1].toHuman();
  
      if (key[0].Token) {
        tokenIssuanceMap.set(key[0].Token, value);
      }
    });
  
    return tokenIssuanceMap.get(token);
  }

  async function vTokenIssuance(api, token) {
    const totalIssuance = await api.query.tokens.totalIssuance({vToken:token});
  
    return totalIssuance;
  }
  

module.exports = {
  tokenPool: tokenPool,
  bifrosttokenIssuance:tokenIssuance,
  vTokenIssuance:vTokenIssuance,
};

// {
//   "total_backable": "250465499708802373",
//   "total_issued": "223466021908860328",
//   "decimal": 12,
//   "token": "KSM"
//   }